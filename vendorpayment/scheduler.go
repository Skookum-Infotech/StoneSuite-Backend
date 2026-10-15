// vendorpayment/scheduler.go — spec AD-9 (as amended): date-triggered dispatch
// of scheduled vendor payments. This file has no goroutine and no ticker: Fly
// scales the app to zero, so an in-process ticker would only fire while a user
// happened to keep a machine awake. Instead an external Cloudflare Cron Trigger
// calls the signed POST /api/internal/automation/tick, whose automation.Runner
// fans out to every servable tenant and calls RunSchedulerTick with that
// tenant's local calendar date.
package vendorpayment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dateLayout is the yyyy-mm-dd form bound to the ::date parameter.
const dateLayout = "2006-01-02"

// TickResult counts what one scheduler tick did for one tenant.
type TickResult struct {
	Transitioned int
	Skipped      int
}

// DueScheduled returns the uuids of live vendor payments currently at status
// SCHD whose scheduled date has arrived (<= today, the tenant's local calendar
// date — never the database server's UTC CURRENT_DATE), ordered by internal id
// for a deterministic dispatch order.
func DueScheduled(ctx context.Context, pool *pgxpool.Pool, today time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT vp.vendor_payment_uuid
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_deleted_at IS NULL
		  AND rs.record_status_code = 'SCHD'
		  AND vp.vendor_payment_scheduled_date <= $1::date
		ORDER BY vp.vendor_payment_id`, today.Format(dateLayout))
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

// RunSchedulerTick transitions every SCHD vendor payment due on or before
// today to SENT, acting as the system actor. A failure on one payment does not
// abort the tick — each is attempted independently and errors accumulate via
// errors.Join, so one stuck payment can never block the rest of the tenant's
// scheduled payments from dispatching. A payment that left SCHD (or was
// deleted) between the listing and its transition — a concurrent tick or a
// user got there first — is counted as skipped, not failed.
func RunSchedulerTick(ctx context.Context, pool *pgxpool.Pool, today time.Time) (TickResult, error) {
	var res TickResult
	due, err := DueScheduled(ctx, pool, today)
	if err != nil {
		return res, err
	}
	var errs error
	for _, uuid := range due {
		_, err := Transition(ctx, pool, uuid, "SENT", systemEmployeeID)
		switch {
		case err == nil:
			res.Transitioned++
		case errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrNotFound):
			res.Skipped++
		case errors.Is(err, ErrApprovalRequired):
			// Approvers configured at SCHD block the automatic move on every
			// tick until someone approves; surface it rather than stay silent.
			slog.Warn("vendor payment scheduler: approval required before dispatch", "vendor_payment", uuid)
			errs = errors.Join(errs, fmt.Errorf("transition vendor payment %s to SENT: %w", uuid, err))
		default:
			errs = errors.Join(errs, fmt.Errorf("transition vendor payment %s to SENT: %w", uuid, err))
		}
	}
	return res, errs
}
