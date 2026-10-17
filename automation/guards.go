package automation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Delivery skip reasons written by the drain guards and the pre-claim sweeps.
const (
	reasonKillSwitch = "kill_switch"
	reasonSuppressed = "suppressed"
	reasonCap        = "cap"
	reasonSuperseded = "superseded"
	reasonStale      = "stale"
	reasonNotRender  = "not_rendered"
)

const (
	// defaultDailyExternalCap and defaultWeeklyContactCap apply only when the
	// automation_settings singleton row is missing (the schema seeds it).
	defaultDailyExternalCap = 200
	defaultWeeklyContactCap = 3
	// weeklyCapWindow is the look-back for the per-contact cap.
	weeklyCapWindow = 7 * 24 * time.Hour
	// staleAfter is how long an unsent planned/failed row may wait before the
	// drain gives up on it.
	staleAfter = 48 * time.Hour
	// maxAttempts is the send-attempt ceiling; reaching it makes a row dead.
	maxAttempts = 5
	// killSwitchRecheck is how long past the next send window a kill-switched
	// group waits before it is re-checked.
	killSwitchRecheck = time.Hour
	// maxLastError bounds the stored last_error text.
	maxLastError = 500
)

// isDeferral reports whether a guard reason postpones a delivery (cap, kill
// switch) rather than dropping it.
func isDeferral(reason string) bool {
	return reason == reasonCap || reason == reasonKillSwitch
}

// retryBackoff is the wait after the 1st and 2nd failed attempts; every later
// attempt waits retryBackoffLong.
var retryBackoff = [...]time.Duration{5 * time.Minute, 30 * time.Minute}

const retryBackoffLong = 2 * time.Hour

// backoffFor returns the delay before retrying after the attempts-th failure.
func backoffFor(attempts int) time.Duration {
	if attempts >= 1 && attempts <= len(retryBackoff) {
		return retryBackoff[attempts-1]
	}
	return retryBackoffLong
}

// isTimeoutErr reports whether a send error is a timeout, i.e. the request may
// still have reached notify, so the outcome is unknown and must not be retried.
func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// truncateError shortens an error message to maxLastError bytes (on a rune boundary).
func truncateError(msg string) string {
	if len(msg) <= maxLastError {
		return msg
	}
	cut := maxLastError
	for cut > 0 && !isRuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// capReached reports whether count has used up limit.
func capReached(count, limit int) bool { return count >= limit }

// localDayStart is the instant of local midnight for the calendar date of today.
func localDayStart(today time.Time, loc *time.Location) time.Time {
	y, m, d := today.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// automationSettings is the per-tenant singleton row.
type automationSettings struct {
	KillSwitch bool
	DailyCap   int
	WeeklyCap  int
}

// loadSettings reads automation_settings, falling back to the schema defaults
// when the singleton row is absent.
func loadSettings(ctx context.Context, q Querier) (automationSettings, error) {
	s := automationSettings{DailyCap: defaultDailyExternalCap, WeeklyCap: defaultWeeklyContactCap}
	err := q.QueryRow(ctx, `SELECT kill_switch, daily_external_cap, per_contact_weekly_cap
FROM automation_settings WHERE id`).Scan(&s.KillSwitch, &s.DailyCap, &s.WeeklyCap)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("load automation settings: %w", err)
	}
	return s, nil
}

// guardExternal applies the per-recipient guards in order: tenant kill switch,
// suppression list, per-contact weekly cap. It returns the skip reason, or ""
// when the email may be sent. The weekly cap counts DISTINCT digest_key among
// sent and unknown-outcome rows (an unknown send may have been delivered) in the
// 7 days before now, so one digest email counts once. Unknown rows have no
// sent_at, so their updated_at stands in.
func guardExternal(ctx context.Context, q Querier, s automationSettings, email string, now time.Time) (string, error) {
	if s.KillSwitch {
		return reasonKillSwitch, nil
	}
	addr := strings.ToLower(strings.TrimSpace(email))
	var suppressed bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM automation_suppressions WHERE email = $1)`, addr).Scan(&suppressed); err != nil {
		return "", fmt.Errorf("check suppression: %w", err)
	}
	if suppressed {
		return reasonSuppressed, nil
	}
	var weekly int
	if err := q.QueryRow(ctx, `
SELECT count(DISTINCT digest_key) FROM automation_deliveries
WHERE status IN ('sent', 'unknown') AND lower(recipient_email) = $1 AND COALESCE(sent_at, updated_at) >= $2 -- lower() so mixed-case history counts; not index-assisted`,
		addr, now.Add(-weeklyCapWindow)).Scan(&weekly); err != nil {
		return "", fmt.Errorf("count weekly sends: %w", err)
	}
	if capReached(weekly, s.WeeklyCap) {
		return reasonCap, nil
	}
	return "", nil
}

// guardDailyCap applies the tenant-wide daily external cap: distinct digests
// sent or of unknown outcome since dayStart (tenant-local midnight). It returns reasonCap or "".
func guardDailyCap(ctx context.Context, q Querier, s automationSettings, dayStart time.Time) (string, error) {
	var today int
	if err := q.QueryRow(ctx, `
SELECT count(DISTINCT digest_key) FROM automation_deliveries
WHERE status IN ('sent', 'unknown') AND recipient_kind = 'external' AND COALESCE(sent_at, updated_at) >= $1`, dayStart).Scan(&today); err != nil {
		return "", fmt.Errorf("count daily sends: %w", err)
	}
	if capReached(today, s.DailyCap) {
		return reasonCap, nil
	}
	return "", nil
}
