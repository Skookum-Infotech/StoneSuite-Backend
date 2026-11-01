package customertransactions

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dateonly"
	"stonesuite-backend/query"
)

// maxSearchLen bounds the free-text search box.
const maxSearchLen = 100

var (
	// statusCodeRe is the shape of a record-status code.
	statusCodeRe = regexp.MustCompile(`^[A-Z_]{2,8}$`)
)

// InvalidParamError reports a malformed request parameter; handlers map it to 400.
type InvalidParamError struct {
	Param string
	Msg   string
}

// Error implements error.
func (e *InvalidParamError) Error() string { return e.Param + ": " + e.Msg }

// Params is one customer Transactions page request.
type Params struct {
	IdentityID   string         // control-plane identity of the caller (from the JWT)
	CustomerUUID string         // the customer whose transactions to list
	Type         string         // optional registry key to narrow to one document type
	Status       string         // optional record-status code
	Search       string         // optional contains-match on the document number
	From, To     *dateonly.Date // optional inclusive bounds on the document date
	OpenOnly     bool           // only records still open
	Page         int            // 1-based; 0 means the first page
	Limit        int            // page size, clamped to [1, MaxLimit]; 0 means DefaultLimit
	Today        time.Time      // tenant-local today, for overdue
}

// Row is one transaction on the customer's list.
type Row struct {
	Type       string         `json:"type"`      // registry key, e.g. "sales_order"
	TypeLabel  string         `json:"typeLabel"` // display name, e.g. "Sales Order"
	Domain     string         `json:"domain"`    // frontend route segment 1
	Module     string         `json:"module"`    // frontend route segment 2
	ID         string         `json:"id"`
	Number     string         `json:"number"`
	Status     string         `json:"status"`
	StatusCode string         `json:"statusCode"`
	Date       dateonly.Date  `json:"date"`
	DueDate    *dateonly.Date `json:"dueDate"`
	Amount     *float64       `json:"amount"`  // nil when the type has no single total
	Balance    *float64       `json:"balance"` // nil when the type has no balance
	IsOpen     bool           `json:"isOpen"`
	IsOverdue  bool           `json:"isOverdue"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// TypeInfo is one document type the caller may read, for the type filter.
type TypeInfo struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Domain string `json:"domain"`
	Module string `json:"module"`
}

// Page is one page of the customer's transactions, newest-dated first.
type Page struct {
	Rows  []Row
	Total int // rows matching the filters across all pages
	Page  int // the page returned (1-based)
	Limit int // the page size used
}

// parse validates p into a page filter, rejecting unknown types and statuses,
// inverted date ranges and out-of-range paging.
func parse(p Params) (pageFilter, int, error) {
	f := pageFilter{limit: p.Limit, status: p.Status, from: p.From, to: p.To, openOnly: p.OpenOnly}
	if p.Type != "" {
		if _, ok := sourceByKey(p.Type); !ok {
			return pageFilter{}, 0, &InvalidParamError{Param: "type", Msg: "unknown transaction type"}
		}
	}
	if f.status != "" && !statusCodeRe.MatchString(f.status) {
		return pageFilter{}, 0, &InvalidParamError{Param: "status", Msg: "not a status code"}
	}
	if f.from != nil && f.to != nil && f.from.After(f.to.Time) {
		return pageFilter{}, 0, &InvalidParamError{Param: "from", Msg: "must not be after to"}
	}
	if f.limit <= 0 {
		f.limit = DefaultLimit
	}
	if f.limit > MaxLimit {
		f.limit = MaxLimit
	}
	page := p.Page
	switch {
	case page < 0 || page > MaxPage:
		return pageFilter{}, 0, &InvalidParamError{Param: "page", Msg: fmt.Sprintf("must be between 1 and %d", MaxPage)}
	case page == 0:
		page = 1
	}
	f.offset = (page - 1) * f.limit
	search := strings.TrimSpace(p.Search)
	if len(search) > maxSearchLen {
		return pageFilter{}, 0, &InvalidParamError{Param: "q", Msg: "search term is too long"}
	}
	f.search = query.StripLikeMeta(search)
	return f, page, nil
}

// List returns one page of the customer's transactions across every document
// type the caller may read. RBAC is fail-closed per type: a type without a read
// grant contributes nothing, and an own-scoped grant also requires the caller to
// own the record (matching the single-record guard, so a listed row never opens
// to a 404). Total counts every matching row so the client can render page
// numbers; a page past the end returns no rows but the true total.
func List(ctx context.Context, pool *pgxpool.Pool, p Params) (Page, error) {
	f, pageNo, err := parse(p)
	if err != nil {
		return Page{}, err
	}
	sc, err := loadScope(ctx, pool, p.IdentityID, p.CustomerUUID)
	if err != nil {
		return Page{}, err
	}
	page := Page{Rows: []Row{}, Page: pageNo, Limit: f.limit}
	queried := narrowTo(sc.sources, p.Type)
	if len(queried) == 0 {
		return page, nil
	}

	rows, total, err := fetchPage(ctx, pool, queried, f, sc, p.Today)
	if err != nil {
		return Page{}, err
	}
	if len(rows) == 0 && f.offset > 0 {
		if total, err = fetchTotal(ctx, pool, queried, f, sc, p.Today); err != nil {
			return Page{}, err
		}
	}
	page.Rows, page.Total = rows, total
	return page, nil
}

// fetchPage runs the page query, returning its rows and the filtered total.
func fetchPage(ctx context.Context, pool *pgxpool.Pool, sources []granted, f pageFilter, sc scope, today time.Time) ([]Row, int, error) {
	sql, args := buildPageSQL(sources, f, sc.customerID, sc.employee, today)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list customer transactions: %w", err)
	}
	defer rows.Close()

	byKey := make(map[string]Source, len(sources))
	for _, g := range sources {
		byKey[g.Key] = g.Source
	}
	out := make([]Row, 0, f.limit)
	total := 0
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Type, &r.ID, &r.Number, &r.Status, &r.StatusCode, &r.Date, &r.DueDate,
			&r.Amount, &r.Balance, &r.IsOpen, &r.IsOverdue, &r.CreatedAt, &r.UpdatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan customer transaction: %w", err)
		}
		src := byKey[r.Type]
		r.TypeLabel, r.Domain, r.Module = src.Label, src.Domain, src.Module
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list customer transactions: %w", err)
	}
	return out, total, nil
}

// fetchTotal counts the rows matching the filters, for a page past the end.
func fetchTotal(ctx context.Context, pool *pgxpool.Pool, sources []granted, f pageFilter, sc scope, today time.Time) (int, error) {
	sql, args := buildCountSQL(sources, f, sc.customerID, sc.employee, today)
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count customer transactions: %w", err)
	}
	return n, nil
}
