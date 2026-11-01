package vendortransactions

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

// statusCodeRe is the shape of a record-status code.
var statusCodeRe = regexp.MustCompile(`^[A-Z_]{2,8}$`)

// InvalidParamError reports a malformed request parameter; handlers map it to 400.
type InvalidParamError struct {
	Param string
	Msg   string
}

// Error implements error.
func (e *InvalidParamError) Error() string { return e.Param + ": " + e.Msg }

// Params is one vendor Transactions page request.
type Params struct {
	IdentityID string         // control-plane identity of the caller (from the JWT)
	VendorUUID string         // the vendor whose transactions to list
	Type       string         // optional registry key to narrow to one document type
	Status     string         // optional record-status code
	Search     string         // optional contains-match on the document number
	From, To   *dateonly.Date // optional inclusive bounds on the document date
	OpenOnly   bool           // only records still open
	Cursor     string         // opaque keyset token from a previous page; "" for the first
	Limit      int            // page size, clamped to [1, MaxLimit]; 0 means DefaultLimit
	Today      time.Time      // tenant-local today, for overdue
}

// Row is one transaction on the vendor's list.
type Row struct {
	Type       string         `json:"type"`      // registry key, e.g. "vendor_bill"
	TypeLabel  string         `json:"typeLabel"` // display name, e.g. "Vendor Bill"
	Domain     string         `json:"domain"`    // frontend route segment 1
	Module     string         `json:"module"`    // frontend route segment 2
	ID         string         `json:"id"`        // the document uuid
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

// Page is one keyset page of the vendor's transactions, newest-created first.
type Page struct {
	Rows       []Row
	NextCursor string // token for the next page; "" when this is the last
	Limit      int    // the page size used
	// Denied is true when the request named a registered type the caller may not
	// read, so handlers can log the denial; the page is empty.
	Denied bool
}

// parse validates p into a page filter, rejecting unknown types and statuses,
// inverted date ranges, bad cursors and over-long searches.
func parse(p Params) (pageFilter, error) {
	f := pageFilter{limit: p.Limit, status: p.Status, from: p.From, to: p.To, openOnly: p.OpenOnly}
	if p.Type != "" {
		if _, ok := sourceByKey(p.Type); !ok {
			return pageFilter{}, &InvalidParamError{Param: "type", Msg: "unknown transaction type"}
		}
	}
	if f.status != "" && !statusCodeRe.MatchString(f.status) {
		return pageFilter{}, &InvalidParamError{Param: "status", Msg: "not a status code"}
	}
	if f.from != nil && f.to != nil && f.from.After(f.to.Time) {
		return pageFilter{}, &InvalidParamError{Param: "from", Msg: "must not be after to"}
	}
	if f.limit <= 0 {
		f.limit = DefaultLimit
	}
	if f.limit > MaxLimit {
		f.limit = MaxLimit
	}
	if p.Cursor != "" {
		c, ok := decodeCursor(p.Cursor)
		if !ok {
			return pageFilter{}, &InvalidParamError{Param: "cursor", Msg: "not a valid cursor"}
		}
		f.after = &c
	}
	search := strings.TrimSpace(p.Search)
	if len(search) > maxSearchLen {
		return pageFilter{}, &InvalidParamError{Param: "q", Msg: "search term is too long"}
	}
	f.search = query.StripLikeMeta(search)
	return f, nil
}

// List returns one keyset page of the vendor's transactions across every
// document type the caller may read. RBAC is fail-closed per type: a type
// without a read grant contributes nothing, and an own-scoped grant also
// requires the caller to own the record (matching the single-record guard, so a
// listed row never opens to a 404).
func List(ctx context.Context, pool *pgxpool.Pool, p Params) (Page, error) {
	f, err := parse(p)
	if err != nil {
		return Page{}, err
	}
	rows, denied, err := queryRows(ctx, pool, p, f)
	if err != nil {
		return Page{}, err
	}
	page := Page{Rows: []Row{}, Limit: f.limit, Denied: denied}
	if len(rows) > f.limit {
		rows = rows[:f.limit]
		last := rows[len(rows)-1]
		page.NextCursor = cursor{createdAt: last.CreatedAt, id: last.ID}.encode()
	}
	if len(rows) > 0 {
		page.Rows = rows
	}
	return page, nil
}

// queryRows is the single scope+filter path shared by List and the export: it
// loads the caller's scope and runs the page query for f, returning up to
// f.limit+1 rows. denied is true when a named type is not readable.
func queryRows(ctx context.Context, pool *pgxpool.Pool, p Params, f pageFilter) ([]Row, bool, error) {
	sc, err := loadScope(ctx, pool, p.IdentityID, p.VendorUUID)
	if err != nil {
		return nil, false, err
	}
	queried := narrowTo(sc.sources, p.Type)
	if len(queried) == 0 {
		return nil, p.Type != "", nil
	}
	rows, err := fetchPage(ctx, pool, queried, f, sc, p.Today)
	if err != nil {
		return nil, false, err
	}
	return rows, false, nil
}

// fetchPage runs the page query, returning up to limit+1 rows.
func fetchPage(ctx context.Context, pool *pgxpool.Pool, sources []granted, f pageFilter, sc scope, today time.Time) ([]Row, error) {
	sql, args := buildPageSQL(sources, f, sc.vendorID, sc.employee, today)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list vendor transactions: %w", err)
	}
	defer rows.Close()

	byKey := make(map[string]Source, len(sources))
	for _, g := range sources {
		byKey[g.Key] = g.Source
	}
	out := make([]Row, 0, f.limit+1)
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Type, &r.ID, &r.Number, &r.Status, &r.StatusCode, &r.Date, &r.DueDate,
			&r.Amount, &r.Balance, &r.IsOpen, &r.IsOverdue, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor transaction: %w", err)
		}
		src := byKey[r.Type]
		r.TypeLabel, r.Domain, r.Module = src.Label, src.Domain, src.Module
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list vendor transactions: %w", err)
	}
	return out, nil
}
