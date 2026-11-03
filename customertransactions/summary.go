package customertransactions

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Summary is the customer's live accounts-receivable position. A money or count
// field is nil when the caller cannot read the module that backs it.
type Summary struct {
	OpenBalance         *float64   `json:"openBalance"`
	OpenInvoiceCount    *int       `json:"openInvoiceCount"`
	OverdueBalance      *float64   `json:"overdueBalance"`
	OverdueInvoiceCount *int       `json:"overdueInvoiceCount"`
	UnappliedCredits    *float64   `json:"unappliedCredits"` // payment + credit memo balances
	Billed12m           *float64   `json:"billed12m"`
	Paid12m             *float64   `json:"paid12m"`
	LastActivityAt      *time.Time `json:"lastActivityAt"` // latest update across readable types
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

// Overview is the unfiltered context around the list: the AR summary, per-type
// counts and the types the caller may read.
type Overview struct {
	Summary      Summary    `json:"summary"`
	Counts       []Count    `json:"counts"`
	Types        []TypeInfo `json:"types"`
	ScopeLimited bool       `json:"scopeLimited"` // some types are unreadable or own-scoped, so figures are partial
}

// GetOverview returns the customer's AR summary, per-type counts and readable
// types. It is independent of any list filter or page, so the client fetches it
// once per visit rather than on every page turn. Figures cover only what the
// caller may read; a caller who can read nothing gets an empty overview.
func GetOverview(ctx context.Context, pool *pgxpool.Pool, identityID, customerUUID string, today time.Time) (Overview, error) {
	sc, err := loadScope(ctx, pool, identityID, customerUUID)
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
	if err := fetchReceivables(ctx, pool, sc, today, &out.Summary); err != nil {
		return Overview{}, err
	}
	if err := fetchCredits(ctx, pool, sc, today, &out.Summary); err != nil {
		return Overview{}, err
	}
	return out, nil
}

// fetchReceivables fills the invoice-derived figures when invoices are readable.
func fetchReceivables(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time, s *Summary) error {
	inv, ok := sc.byKey(keyInvoice)
	if !ok {
		return nil
	}
	sql, args := buildReceivablesSQL(inv, sc.customerID, sc.employee, today)
	var open, overdue, billed float64
	var openN, overdueN int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&open, &openN, &overdue, &overdueN, &billed); err != nil {
		return fmt.Errorf("summarise customer receivables: %w", err)
	}
	s.OpenBalance, s.OpenInvoiceCount = &open, &openN
	s.OverdueBalance, s.OverdueInvoiceCount = &overdue, &overdueN
	s.Billed12m = &billed
	return nil
}

// fetchCredits fills unapplied credit and payments received, from whichever of
// payments and credit memos are readable.
func fetchCredits(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time, s *Summary) error {
	var subset []granted
	for _, key := range []string{keyPayment, keyCreditMemo} {
		if g, ok := sc.byKey(key); ok {
			subset = append(subset, g)
		}
	}
	if len(subset) == 0 {
		return nil
	}
	sql, args := buildCreditsSQL(subset, sc.customerID, sc.employee, today)
	var credits, paid float64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&credits, &paid); err != nil {
		return fmt.Errorf("summarise customer credits: %w", err)
	}
	s.UnappliedCredits = &credits
	if _, ok := sc.byKey(keyPayment); ok {
		s.Paid12m = &paid
	}
	return nil
}

// fetchCounts returns the per-type counts in registry order plus the latest
// update across everything readable.
func fetchCounts(ctx context.Context, pool *pgxpool.Pool, sc scope, today time.Time) ([]Count, *time.Time, error) {
	sql, args := buildCountsSQL(sc.sources, sc.customerID, sc.employee, today)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("count customer transactions: %w", err)
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
			return nil, nil, fmt.Errorf("scan customer transaction count: %w", err)
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
		return nil, nil, fmt.Errorf("count customer transactions: %w", err)
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
