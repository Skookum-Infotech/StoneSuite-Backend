package tenancy

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// SessionTimeouts are Postgres session limits set on every pooled connection.
// Without them one slow or lock-blocked query held a pool connection (5 per
// tenant, 10 for the control plane) indefinitely. A zero field leaves that
// setting at the server default.
type SessionTimeouts struct {
	Statement time.Duration // statement_timeout
	Lock      time.Duration // lock_timeout
	IdleInTx  time.Duration // idle_in_transaction_session_timeout
}

// setSessionTimeoutsSQL applies all three in one round trip. set_config takes
// parameters, so nothing is interpolated; an empty value would reset a
// setting, so zero fields pass the current value back unchanged.
const setSessionTimeoutsSQL = `SELECT
	set_config('statement_timeout', COALESCE($1, current_setting('statement_timeout')), false),
	set_config('lock_timeout', COALESCE($2, current_setting('lock_timeout')), false),
	set_config('idle_in_transaction_session_timeout', COALESCE($3, current_setting('idle_in_transaction_session_timeout')), false)`

// IsZero reports whether no limit is configured.
func (t SessionTimeouts) IsZero() bool {
	return t.Statement <= 0 && t.Lock <= 0 && t.IdleInTx <= 0
}

// apply sets the configured limits on conn. It runs in AfterConnect rather
// than as startup RuntimeParams because a PgBouncer pooler endpoint can
// reject unknown startup parameters.
func (t SessionTimeouts) apply(ctx context.Context, conn *pgx.Conn) error {
	if t.IsZero() {
		return nil
	}
	if _, err := conn.Exec(ctx, setSessionTimeoutsSQL, millis(t.Statement), millis(t.Lock), millis(t.IdleInTx)); err != nil {
		return fmt.Errorf("set session timeouts: %w", err)
	}
	return nil
}

// millis renders d as a Postgres duration in milliseconds, or nil (keep the
// current value) when d is not positive.
func millis(d time.Duration) *string {
	if d <= 0 {
		return nil
	}
	s := strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	return &s
}
