package automation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// vendorBillJobName identifies the vendor-bill-due reminder job in logs.
const vendorBillJobName = "vendor_bill_reminders"

// VendorBillReminders reminds vendor bill owners before and at the payment due date.
type VendorBillReminders struct{ deps Deps }

// Name identifies the job in logs.
func (*VendorBillReminders) Name() string { return vendorBillJobName }

// Run plans and drains the vendor_bill_due rule; a missing or disabled rule is a
// no-op that writes nothing.
func (j *VendorBillReminders) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	rule, enabled, err := loadRule(ctx, pool, RuleKeyVendorBillDue)
	if err != nil {
		return Result{}, fmt.Errorf("vendor bill reminders: %w", err)
	}
	if !enabled {
		return Result{}, nil
	}
	_, skipped, err := planVendorBills(ctx, pool, env, rule)
	if err != nil {
		return Result{}, fmt.Errorf("vendor bill reminders plan: %w", err)
	}
	res, err := drain(ctx, pool, env, j.deps.Send, vendorBillRenderer{pool: pool}, rule.ID)
	res.Skipped += skipped
	if err != nil {
		return res, fmt.Errorf("vendor bill reminders drain: %w", err)
	}
	return res, nil
}
