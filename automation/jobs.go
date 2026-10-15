package automation

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/vendorpayment"
)

// VendorPaymentDispatch moves vendor payments in SCHD whose scheduled date has
// arrived to SENT (spec AD-9).
type VendorPaymentDispatch struct{}

// Name identifies the job in logs.
func (VendorPaymentDispatch) Name() string { return "vendor_payment_dispatch" }

// Run dispatches the tenant's due scheduled vendor payments.
func (VendorPaymentDispatch) Run(ctx context.Context, pool *pgxpool.Pool, today time.Time) (Result, error) {
	r, err := vendorpayment.RunSchedulerTick(ctx, pool, today)
	return Result{Transitioned: r.Transitioned, Skipped: r.Skipped}, err
}

// DefaultJobs is every date-triggered job the tick endpoint runs.
func DefaultJobs() []Job {
	return []Job{VendorPaymentDispatch{}}
}
