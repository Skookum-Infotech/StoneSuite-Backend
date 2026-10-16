package automation

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/vendorpayment"
)

// VendorPaymentDispatch moves vendor payments in SCHD whose scheduled date has
// arrived to SENT (spec AD-9).
type VendorPaymentDispatch struct{}

// Name identifies the job in logs.
func (VendorPaymentDispatch) Name() string { return "vendor_payment_dispatch" }

// Run dispatches the tenant's due scheduled vendor payments.
func (VendorPaymentDispatch) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	r, err := vendorpayment.RunSchedulerTick(ctx, pool, env.Today)
	return Result{Transitioned: r.Transitioned, Skipped: r.Skipped}, err
}

// Deps are the injected collaborators jobs need beyond the tenant database.
type Deps struct {
	// Send delivers outbound notifications.
	Send Sender
	// LinkURL builds the public link to a tenant record.
	LinkURL func(tenantID, recordID string) string
}

// DefaultJobs is every date-triggered job the tick endpoint runs.
func DefaultJobs(d Deps) []Job {
	return []Job{VendorPaymentDispatch{}}
}
