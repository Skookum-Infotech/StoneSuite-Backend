// vendorpayment/scheduler.go — spec AD-9: date-triggered dispatch of
// scheduled vendor payments. This file has no goroutine and no ticker; the
// per-tenant scheduling loop that calls RunSchedulerTick on a cadence is
// wired in main.go alongside startRAGIndexing (spec AD-9's reference
// pattern) in a later step.
package vendorpayment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DueScheduled returns the uuids of live vendor payments currently at status
// SCHD whose scheduled date has arrived (<= CURRENT_DATE), ordered by
// internal id for a deterministic dispatch order.
func DueScheduled(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT vp.vendor_payment_uuid
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_deleted_at IS NULL
		  AND rs.record_status_code = 'SCHD'
		  AND vp.vendor_payment_scheduled_date <= CURRENT_DATE
		ORDER BY vp.vendor_payment_id`)
	if err != nil {
		return nil, fmt.Errorf("list due scheduled vendor payments: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan due scheduled vendor payment: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due scheduled vendor payments: %w", err)
	}
	return out, nil
}

// RunSchedulerTick transitions every due SCHD vendor payment to SENT, acting
// as the system actor. A failure on one payment does not abort the tick —
// each is attempted independently and errors accumulate via errors.Join, so
// one stuck payment can never block the rest of the tenant's scheduled
// payments from dispatching. Returns the count of payments successfully
// transitioned.
func RunSchedulerTick(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	due, err := DueScheduled(ctx, pool)
	if err != nil {
		return 0, err
	}
	var succeeded int
	var errs error
	for _, uuid := range due {
		if _, err := Transition(ctx, pool, uuid, "SENT", systemEmployeeID); err != nil {
			errs = errors.Join(errs, fmt.Errorf("transition vendor payment %s to SENT: %w", uuid, err))
			continue
		}
		succeeded++
	}
	return succeeded, errs
}
