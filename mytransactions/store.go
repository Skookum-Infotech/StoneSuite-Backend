package mytransactions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
)

// maxSearchLen bounds the free-text search box.
const maxSearchLen = 100

// recentWindow is the "touched recently" stat-card window.
const recentWindow = 7 * 24 * time.Hour

// ErrNoAccess is returned when the caller may read none of the record types.
var ErrNoAccess = errors.New("no readable record types")

// InvalidParamError reports a malformed request parameter; handlers map it to 400.
type InvalidParamError struct {
	Param string
	Msg   string
}

// Error implements error.
func (e *InvalidParamError) Error() string { return e.Param + ": " + e.Msg }

// Params is one My Transactions page request.
type Params struct {
	IdentityID string // control-plane identity of the caller (from the JWT)
	Role       string // "", all, created or updated
	Type       string // optional registry key to narrow to one record type
	Search     string // optional contains-match on number, name and account
	Page       int    // 1-based; 0 means the first page
	Limit      int    // page size, clamped to [1, MaxLimit]; 0 means DefaultLimit
}

// Row is one record on the caller's list.
type Row struct {
	Type       string    `json:"type"`      // registry key, e.g. "sales_order"
	TypeLabel  string    `json:"typeLabel"` // display name, e.g. "Sales Order"
	Domain     string    `json:"domain"`    // frontend route segment 1
	Module     string    `json:"module"`    // frontend route segment 2
	ID         string    `json:"id"`
	Number     string    `json:"number"`
	Name       string    `json:"name"`
	Account    string    `json:"account"`
	Status     string    `json:"status"`
	StatusCode string    `json:"statusCode"`
	Amount     *float64  `json:"amount"` // nil when the module has no single total
	Role       string    `json:"role"`   // RoleCreated | RoleUpdated
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// TypeInfo is one record type the caller may read, for the page's type filter.
type TypeInfo struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Domain string `json:"domain"`
}

// Summary is the stat-card counts over everything the caller may read.
type Summary struct {
	Total   int `json:"total"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Recent  int `json:"recent"` // touched within recentWindow
}

// Page is one page of the caller's records, newest-updated first.
type Page struct {
	Rows  []Row
	Total int // rows matching the filters across all pages
	Page  int // the page returned (1-based)
	Limit int // the page size used
}

// Overview is the unfiltered context around the list: stat-card counts and the
// record types the caller may read.
type Overview struct {
	Summary Summary
	Types   []TypeInfo
}

// parse validates p into a page filter, rejecting unknown roles and types and
// out-of-range paging.
func parse(p Params) (pageFilter, int, error) {
	f := pageFilter{role: p.Role, limit: p.Limit}
	switch f.role {
	case "":
		f.role = RoleAll
	case RoleAll, RoleCreated, RoleUpdated:
	default:
		return pageFilter{}, 0, &InvalidParamError{Param: "role", Msg: "must be one of all, created, updated"}
	}
	if p.Type != "" {
		if _, ok := sourceByKey(p.Type); !ok {
			return pageFilter{}, 0, &InvalidParamError{Param: "type", Msg: "unknown record type"}
		}
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

// actor is the caller as the data columns reference them.
type actor struct {
	userArg  *string // tenant users.id; nil binds SQL NULL so the audit match finds nothing
	employee int     // 0 matches no row (ids start at 1) when there is no employee profile
	found    bool    // false when the identity has no user in this tenant
}

// resolveActor maps a control-plane identity to the tenant user id and
// employee id the data columns reference.
func resolveActor(ctx context.Context, pool *pgxpool.Pool, identityID string) (actor, error) {
	var (
		userID   string
		employee int
	)
	err := pool.QueryRow(ctx, `
		SELECT u.id::text, COALESCE(e.employee_id, 0)
		FROM users u
		LEFT JOIN employee e ON e.employee_user_id = u.id AND e.employee_deleted_at IS NULL
		WHERE u.identity_id = $1`, identityID).Scan(&userID, &employee)
	if errors.Is(err, pgx.ErrNoRows) {
		return actor{}, nil
	}
	if err != nil {
		return actor{}, fmt.Errorf("resolve actor: %w", err)
	}
	return actor{userArg: &userID, employee: employee, found: true}, nil
}

// readableFor returns the sources the identity may read, in registry order.
// It fails with ErrNoAccess when there are none.
func readableFor(ctx context.Context, pool *pgxpool.Pool, identityID string) ([]granted, error) {
	grants, err := authz.EffectiveGrants(ctx, pool, identityID)
	if err != nil {
		return nil, fmt.Errorf("load grants: %w", err)
	}
	var out []granted
	for _, s := range Sources() {
		if g, ok := grantFor(s, grants); ok {
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoAccess
	}
	return out, nil
}

// narrowTo keeps only the source with the given key ("" keeps all).
func narrowTo(all []granted, key string) []granted {
	if key == "" {
		return all
	}
	for _, g := range all {
		if g.Key == key {
			return []granted{g}
		}
	}
	return nil
}

// List returns one page of the records the caller created or last updated,
// across every module they may read. RBAC is fail-closed per module: a module
// without a read grant contributes nothing, and an own-scoped grant also
// requires the caller to own the record (matching the single-record guard, so a
// listed row never opens to a 404). Total counts every matching row so the
// client can render page numbers; a page past the end returns no rows but the
// true total, so the client can step back.
func List(ctx context.Context, pool *pgxpool.Pool, p Params) (Page, error) {
	f, pageNo, err := parse(p)
	if err != nil {
		return Page{}, err
	}
	all, err := readableFor(ctx, pool, p.IdentityID)
	if err != nil {
		return Page{}, err
	}
	who, err := resolveActor(ctx, pool, p.IdentityID)
	if err != nil {
		return Page{}, err
	}
	page := Page{Rows: []Row{}, Page: pageNo, Limit: f.limit}
	queried := narrowTo(all, p.Type)
	if len(queried) == 0 || !who.found {
		return page, nil
	}

	rows, total, err := fetchPage(ctx, pool, queried, f, who)
	if err != nil {
		return Page{}, err
	}
	if len(rows) == 0 && f.offset > 0 {
		if total, err = fetchTotal(ctx, pool, queried, f, who); err != nil {
			return Page{}, err
		}
	}
	page.Rows, page.Total = rows, total
	return page, nil
}

// GetOverview returns the unfiltered stat-card counts and readable record
// types. It is independent of any list filter or page, so the client fetches
// it once per visit rather than on every page turn.
func GetOverview(ctx context.Context, pool *pgxpool.Pool, identityID string) (Overview, error) {
	all, err := readableFor(ctx, pool, identityID)
	if err != nil {
		return Overview{}, err
	}
	out := Overview{Types: typeInfos(all)}
	who, err := resolveActor(ctx, pool, identityID)
	if err != nil || !who.found {
		return out, err
	}
	sql, args := buildSummarySQL(all, time.Now().Add(-recentWindow), who.employee, who.userArg)
	s := &out.Summary
	if err := pool.QueryRow(ctx, sql, args...).Scan(&s.Total, &s.Created, &s.Updated, &s.Recent); err != nil {
		return Overview{}, fmt.Errorf("summarise my transactions: %w", err)
	}
	return out, nil
}

// typeInfos lists the readable types for the page's type filter.
func typeInfos(all []granted) []TypeInfo {
	out := make([]TypeInfo, len(all))
	for i, g := range all {
		out[i] = TypeInfo{Type: g.Key, Label: g.Label, Domain: g.Domain}
	}
	return out
}

// fetchPage runs the page query, returning its rows and the filtered total.
func fetchPage(ctx context.Context, pool *pgxpool.Pool, sources []granted, f pageFilter, who actor) ([]Row, int, error) {
	sql, args := buildPageSQL(sources, f, who.employee, who.userArg)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list my transactions: %w", err)
	}
	defer rows.Close()

	byKey := make(map[string]Source, len(sources))
	for _, g := range sources {
		byKey[g.Key] = g.Source
	}
	out := make([]Row, 0, f.limit)
	total := 0
	for rows.Next() {
		var (
			r         Row
			createdBy bool
		)
		if err := rows.Scan(&r.Type, &r.ID, &r.Number, &r.Name, &r.Account, &r.Status, &r.StatusCode,
			&r.Amount, &createdBy, &r.UpdatedAt, &r.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan my transaction: %w", err)
		}
		src := byKey[r.Type]
		r.TypeLabel, r.Domain, r.Module = src.Label, src.Domain, src.Module
		r.Role = RoleUpdated
		if createdBy {
			r.Role = RoleCreated
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list my transactions: %w", err)
	}
	return out, total, nil
}

// fetchTotal counts the rows matching the filters, for a page past the end.
func fetchTotal(ctx context.Context, pool *pgxpool.Pool, sources []granted, f pageFilter, who actor) (int, error) {
	sql, args := buildCountSQL(sources, f, who.employee, who.userArg)
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count my transactions: %w", err)
	}
	return n, nil
}
