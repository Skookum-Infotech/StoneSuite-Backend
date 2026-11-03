package vendortransactions

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Summary is the vendor's live accounts-payable position. A money or count
// field is nil when the caller cannot read the module that backs it.
type Summary struct {
	OpenBalance      *float64   `json:"openBalance"`
	OpenBillCount    *int       `json:"openBillCount"`
	OverdueAmount    *float64   `json:"overdueAmount"`
	OverdueBillCount *int       `json:"overdueBillCount"`
	UnappliedCredits *float64   `json:"unappliedCredits"` // vendor payment + vendor credit balances
	Paid12m          *float64   `json:"paid12m"`
	LastActivityAt   *time.Time `json:"lastActivityAt"`   // latest update across readable types
	ScheduledOutflow *float64   `json:"scheduledOutflow"` // sum of SCHD vendor payments
	ScheduledCount   *int       `json:"scheduledCount"`
}

// StatusCount is how many of a type's records sit in one status.
type StatusCount struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// Count is one readable type's record counts, for the tab's filter dropdowns.
type Count struct {
	Type      string        `json:"type"`
	Count     int           `json:"count"`
	OpenCount int           `json:"openCount"`
	Statuses  []StatusCount `json:"statuses"`
}

// Overview is the unfiltered context around the list: the AP summary, per-type
// counts and the types the caller may read.
type Overview struct {
	Summary      Summary    `json:"summary"`
	Counts       []Count    `json:"counts"`
	Types        []TypeInfo `json:"types"`
	ScopeLimited bool       `json:"scopeLimited"` // some types are unreadable or own-scoped, so figures are partial
}

// GetOverview returns the vendor's AP summary, per-type counts and readable
// types. It builds its scope with the same loadScope as List, so it covers
// exactly the documents the list shows. It is independent of any list filter or
// page. A caller who can read nothing gets an empty overview.
func GetOverview(ctx context.Context, pool *pgxpool.Pool, identityID, vendorUUID string, today time.Time) (Overview, error) {
	sc, err := loadScope(ctx, pool, identityID, vendorUUID)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{Counts: []Count{}, Types: typeInfos(sc.sources), ScopeLimited: sc.limited()}
	if len(sc.sources) == 0 {
		return out, nil
	}

	var last *time.Time
	if out.Counts, last, err = fetchCounts(ctx, pool, sc, today); err != nil {
		return Overview{}, err
	}
	out.Summary.LastActivityAt = last
	if err := fetchPayables(ctx, pool, sc, today, &out.Summary); err != nil {
		return Overview{}, err
	}
	if err := fetchCredits(ctx, pool, sc, today, &out.Summary); err != nil {
		return Overview{}, err
	}
	if err := fetchBillPayments(ctx, pool, sc, today, &out.Summary); err != nil {
		return Overview{}, err
	}
	if err := fetchScheduled(ctx, pool, sc, today, &out.Summary); err != nil {
		return Overview{}, err
	}
	return out, nil
}

// fetchPayables fills the bill-derived figures when vendor bills are readable.
func fetchPayables(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time, s *Summary) error {
	bill, ok := sc.byKey(keyVendorBill)
	if !ok {
		return nil
	}
	sql, args := buildPayablesSQL(bill, sc.vendorID, sc.employee, today)
	var open, overdue float64
	var openN, overdueN int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&open, &openN, &overdue, &overdueN); err != nil {
		return fmt.Errorf("summarise vendor payables: %w", err)
	}
	s.OpenBalance, s.OpenBillCount = &open, &openN
	s.OverdueAmount, s.OverdueBillCount = &overdue, &overdueN
	return nil
}

// fetchCredits fills unapplied credit and payments made, from whichever of
// vendor payments and vendor credits are readable.
func fetchCredits(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time, s *Summary) error {
	var subset []granted
	for _, key := range []string{keyVendorPayment, keyVendorCredit} {
		if g, ok := sc.byKey(key); ok {
			subset = append(subset, g)
		}
	}
	if len(subset) == 0 {
		return nil
	}
	sql, args := buildCreditsSQL(subset, sc.vendorID, sc.employee, today)
	var credits, paid float64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&credits, &paid); err != nil {
		return fmt.Errorf("summarise vendor credits: %w", err)
	}
	s.UnappliedCredits = &credits
	if _, ok := sc.byKey(keyVendorPayment); ok {
		s.Paid12m = &paid
	}
	return nil
}

// fetchBillPayments adds settlements recorded directly on readable vendor bills
// to the paid-in-12-months figure, so it covers both ways a bill gets paid.
func fetchBillPayments(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time, s *Summary) error {
	bill, ok := sc.byKey(keyVendorBill)
	if !ok {
		return nil
	}
	sql, args := buildBillPaymentsSQL(bill, sc.vendorID, sc.employee, today)
	var direct float64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&direct); err != nil {
		return fmt.Errorf("summarise vendor bill payments: %w", err)
	}
	total := direct
	if s.Paid12m != nil {
		total += *s.Paid12m
	}
	s.Paid12m = &total
	return nil
}

// fetchCounts returns the per-type counts in registry order plus the latest
// update across everything readable.
func fetchCounts(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time) ([]Count, *time.Time, error) {
	sql, args := buildCountsSQL(sc.sources, sc.vendorID, sc.employee, today)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("count vendor transactions: %w", err)
	}
	defer rows.Close()

	byType := map[string]*Count{}
	var last *time.Time
	for rows.Next() {
		var (
			typ, code, label string
			n, open          int
			updated          *time.Time
		)
		if err := rows.Scan(&typ, &code, &label, &n, &open, &updated); err != nil {
			return nil, nil, fmt.Errorf("scan vendor transaction count: %w", err)
		}
		c := byType[typ]
		if c == nil {
			c = &Count{Type: typ, Statuses: []StatusCount{}}
			byType[typ] = c
		}
		c.Count += n
		c.OpenCount += open
		c.Statuses = append(c.Statuses, StatusCount{Code: code, Label: label, Count: n})
		if updated != nil && (last == nil || updated.After(*last)) {
			last = updated
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("count vendor transactions: %w", err)
	}
	return foldCounts(sc.sources, byType), last, nil
}

// foldCounts orders the per-type counts by registry order, keeping only types
// that have records, with each type's statuses sorted by code.
func foldCounts(sources []granted, byType map[string]*Count) []Count {
	out := make([]Count, 0, len(byType))
	for _, g := range sources {
		c, ok := byType[g.Key]
		if !ok {
			continue
		}
		sort.Slice(c.Statuses, func(i, j int) bool { return c.Statuses[i].Code < c.Statuses[j].Code })
		out = append(out, *c)
	}
	return out
}
