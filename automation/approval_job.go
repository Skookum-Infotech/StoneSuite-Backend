package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// approvalJobName identifies the stuck-approval reminder job in logs.
const approvalJobName = "approval_reminders"

// ApprovalReminders reminds approvers who still owe a sign-off on records
// sitting at an approval gate.
type ApprovalReminders struct{ deps Deps }

// Name identifies the job in logs.
func (*ApprovalReminders) Name() string { return approvalJobName }

// Run plans and drains the approval_stuck rule; a missing or disabled rule is a
// no-op that writes nothing.
func (j *ApprovalReminders) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	rule, enabled, err := loadRule(ctx, pool, RuleKeyApprovalStuck)
	if err != nil {
		return Result{}, fmt.Errorf("approval reminders: %w", err)
	}
	if !enabled {
		return Result{}, nil
	}
	_, skipped, err := planApprovals(ctx, pool, env, rule)
	if err != nil {
		return Result{}, fmt.Errorf("approval reminders plan: %w", err)
	}
	res, err := drain(ctx, pool, env, j.deps.Send, approvalRenderer{q: pool}, rule.ID)
	res.Skipped += skipped
	if err != nil {
		return res, fmt.Errorf("approval reminders drain: %w", err)
	}
	return res, nil
}
