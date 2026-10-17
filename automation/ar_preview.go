package automation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/tenancy"
)

// maxPreviewRecipients caps the recipients listed in a preview; Counts stay total.
const maxPreviewRecipients = 200

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
// "planned" or the skip reason (pre_enable, stale, superseded, no_email).
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
// ignored; a NULL enabled_at means "enabling today".
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
	rows, err := buildARPlanRows(ctx, q, env, rule)
	if err != nil {
		return Preview{}, fmt.Errorf("preview ar: %w", err)
	}
	out := Preview{Counts: map[string]int{}, Recipients: []PreviewRecipient{}}
	byEmail := map[string][]PlanRow{}
	for _, r := range rows {
		if r.Status == statusPlanned {
			out.Counts[statusPlanned]++
			byEmail[r.Email] = append(byEmail[r.Email], r)
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
