package vendortransactions

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dateonly"
)

// DefaultScheduledLimit is how many scheduled payments are listed when the
// caller does not ask for a number.
const DefaultScheduledLimit = 5

// ScheduledRow is one vendor payment scheduled for release.
type ScheduledRow struct {
	ID            string         `json:"id"`
	Number        string         `json:"number"`
	ScheduledDate *dateonly.Date `json:"scheduledDate"` // nil when no date was set
	Amount        float64        `json:"amount"`
}

// Scheduled is the scheduled payment list; Denied is true when the caller cannot
// read vendor payments (Rows is then empty).
type Scheduled struct {
	Rows   []ScheduledRow
	Denied bool
}

// fetchScheduled fills the scheduled outflow and count when vendor payments are
// readable. It uses the payment branch, so the vendor, soft-delete and own-scope
// predicates match the list and every other figure.
func fetchScheduled(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time, s *Summary) error {
	pay, ok := sc.byKey(keyVendorPayment)
	if !ok {
		return nil
	}
	sql, args := buildScheduledTotalSQL(pay, sc.vendorID, sc.employee, today)
	var outflow float64
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&outflow, &n); err != nil {
		return fmt.Errorf("summarise scheduled vendor payments: %w", err)
	}
	s.ScheduledOutflow, s.ScheduledCount = &outflow, &n
	return nil
}

// ListScheduled returns the vendor's scheduled payments, soonest first. A limit
// of 0 means DefaultScheduledLimit; a negative or over-MaxLimit value is an
// InvalidParamError. The result is empty when vendor payments are unreadable.
func ListScheduled(ctx context.Context, pool *pgxpool.Pool, identityID, vendorUUID string, limit int) (Scheduled, error) {
	if limit < 0 || limit > MaxLimit {
		return Scheduled{}, &InvalidParamError{Param: "limit", Msg: "must be between 1 and 100"}
	}
	if limit == 0 {
		limit = DefaultScheduledLimit
	}
	sc, err := loadScope(ctx, pool, identityID, vendorUUID)
	if err != nil {
		return Scheduled{}, err
	}
	out := Scheduled{Rows: []ScheduledRow{}}
	pay, ok := sc.byKey(keyVendorPayment)
	if !ok {
		out.Denied = true
		return out, nil
	}
	sql, args := buildScheduledListSQL(pay, sc.vendorID, sc.employee, time.Time{}, limit)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return Scheduled{}, fmt.Errorf("list scheduled vendor payments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ScheduledRow
		var amount *float64
		if err := rows.Scan(&r.ID, &r.Number, &r.ScheduledDate, &amount); err != nil {
			return Scheduled{}, fmt.Errorf("scan scheduled vendor payment: %w", err)
		}
		if amount != nil {
			r.Amount = *amount
		}
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return Scheduled{}, fmt.Errorf("list scheduled vendor payments: %w", err)
	}
	return out, nil
}
