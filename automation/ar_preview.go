package automation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/tenancy"
)

// maxPreviewRecipients caps the recipients listed in a preview; Counts stay total.
const maxPreviewRecipients = 200

// reasonExisting is the Counts key for rows whose occurrence already has a delivery.
const reasonExisting = "existing"

// ErrRuleNotFound is returned by PreviewAR when the rule key has no row.
var ErrRuleNotFound = errors.New("automation rule not found")

// ErrPreviewUnsupported is returned by PreviewAR for an existing rule that has no preview.
var ErrPreviewUnsupported = errors.New("preview not supported for this rule")

// PreviewRecipient groups the planned rows one address would receive.
type PreviewRecipient struct {
	Email string    `json:"email"`
	Items []PlanRow `json:"items"`
}

// Preview is the dry-run outcome of the AR planner. Counts is keyed by
// "planned", "existing" (already has a delivery row) or the skip reason
// (pre_enable, stale, superseded, no_email).
type Preview struct {
	Counts     map[string]int     `json:"counts"`
	Recipients []PreviewRecipient `json:"recipients"`
}

// NewPreviewEnv builds the JobEnv a Runner tick would build for tenant t at now.
func NewPreviewEnv(t *tenancy.Tenant, now time.Time, tzName string, fallback *time.Location) JobEnv {
	return newJobEnv(t, now, tzName, fallback)
}

// PreviewAR computes what the ar_dunning planner would write now, writing nothing.
func PreviewAR(ctx context.Context, pool *pgxpool.Pool, env JobEnv, ruleKey string) (Preview, error) {
	return previewAR(ctx, pool, env, ruleKey)
}

// previewAR is PreviewAR over any read surface. The rule's enabled flag is
// ignored, and so is the stored enabled_at of a disabled rule (stale from an
// earlier enablement); a NULL or ignored enabled_at means "enabling today".
func previewAR(ctx context.Context, q Querier, env JobEnv, ruleKey string) (Preview, error) {
	rule, found, _, err := loadRuleAnyState(ctx, q, ruleKey)
	if err != nil {
		return Preview{}, fmt.Errorf("preview ar: %w", err)
	}
	if !found {
		return Preview{}, ErrRuleNotFound
	}
	if ruleKey != RuleKeyARDunning {
		return Preview{}, ErrPreviewUnsupported
	}
	enabled, err := ruleEnabled(ctx, q, ruleKey)
	if err != nil {
		return Preview{}, fmt.Errorf("preview ar: %w", err)
	}
	if !enabled {
		rule.EnabledOn = time.Time{}
	}
	rows, err := buildARPlanRows(ctx, q, env, rule)
	if err != nil {
		return Preview{}, fmt.Errorf("preview ar: %w", err)
	}
	out := Preview{Counts: map[string]int{}, Recipients: []PreviewRecipient{}}
	existing, err := existingOccurrences(ctx, q, rule.ID, rows)
	if err != nil {
		return Preview{}, fmt.Errorf("preview ar: %w", err)
	}
	byEmail := map[string][]PlanRow{}
	for _, r := range rows {
		if r.Status == statusPlanned {
			if existing[OccurrenceKey(rule.ID, resourceInvoice, r.InvoiceUUID, r.Anchor, r.Stage)] {
				out.Counts[reasonExisting]++
				continue
			}
			out.Counts[statusPlanned]++
			email := strings.ToLower(r.Email)
			byEmail[email] = append(byEmail[email], r)
		} else {
			out.Counts[r.Reason]++
		}
	}
	emails := make([]string, 0, len(byEmail))
	for e := range byEmail {
		emails = append(emails, e)
	}
	sort.Strings(emails)
	if len(emails) > maxPreviewRecipients {
		emails = emails[:maxPreviewRecipients]
	}
	for _, e := range emails {
		out.Recipients = append(out.Recipients, PreviewRecipient{Email: e, Items: byEmail[e]})
	}
	return out, nil
}

// ruleEnabled reports the rule's enabled flag.
func ruleEnabled(ctx context.Context, q Querier, key string) (bool, error) {
	var en bool
	if err := q.QueryRow(ctx, `SELECT enabled FROM automation_rules WHERE rule_key = $1`, key).Scan(&en); err != nil {
		return false, fmt.Errorf("load rule enabled %s: %w", key, err)
	}
	return en, nil
}

// existingOccurrences returns which planned rows already have a delivery row.
func existingOccurrences(ctx context.Context, q Querier, ruleID string, rows []PlanRow) (map[string]bool, error) {
	var keys []string
	for _, r := range rows {
		if r.Status == statusPlanned {
			keys = append(keys, OccurrenceKey(ruleID, resourceInvoice, r.InvoiceUUID, r.Anchor, r.Stage))
		}
	}
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	dbRows, err := q.Query(ctx, `SELECT occurrence_key FROM automation_deliveries WHERE occurrence_key = ANY($1::text[])`, keys)
	if err != nil {
		return nil, fmt.Errorf("preview existing: %w", err)
	}
	defer dbRows.Close()
	for dbRows.Next() {
		var k string
		if err := dbRows.Scan(&k); err != nil {
			return nil, fmt.Errorf("preview existing scan: %w", err)
		}
		out[k] = true
	}
	if err := dbRows.Err(); err != nil {
		return nil, fmt.Errorf("preview existing rows: %w", err)
	}
	return out, nil
}
