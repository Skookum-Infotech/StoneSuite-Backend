package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// Page is one keyset-paginated slice of accounts.
type Page struct {
	Records    []*Account `json:"records"`
	NextCursor string     `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
}

// Filters are the query-param toggles the list endpoint accepts on top of the
// query.Request body. A nil field means "no constraint". The dropdown call
// every transaction screen makes is Postable=true + Active=true.
type Filters struct {
	Postable      *bool
	Active        *bool
	Visible       *bool
	SubCategoryID *int
}

// clauses renders Filters as SQL fragments plus their arguments, starting
// placeholder numbering at startIdx.
func (f Filters) clauses(startIdx int) ([]string, []any) {
	var (
		frags []string
		args  []any
	)
	add := func(expr string, v any) {
		frags = append(frags, fmt.Sprintf("%s $%d", expr, startIdx+len(args)))
		args = append(args, v)
	}
	if f.Postable != nil {
		add("a.coa_account_is_postable =", *f.Postable)
	}
	if f.Active != nil {
		add("a.coa_account_is_active =", *f.Active)
	}
	if f.Visible != nil {
		add("a.coa_account_is_visible =", *f.Visible)
	}
	if f.SubCategoryID != nil {
		add("a.subcategory_id =", *f.SubCategoryID)
	}
	return frags, args
}

// Get returns one live account by public uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Account, error) {
	row := pool.QueryRow(ctx,
		accountSelect+` WHERE `+liveOnly+` AND a.coa_account_uuid = $1`, uuid)
	acct, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	return acct, nil
}

// Search runs filter + sort + global search + keyset pagination through the
// shared query engine. The Filters clauses and the user's filters are ANDed --
// a filter can only ever narrow the result set, never widen it.
//
// Mirrors inventory.Search: query.Build emits Where/Keyset/OrderBy separately,
// and the store fetches EffLimit+1 rows to detect a further page.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request, f Filters) (Page, error) {
	built, err := query.Build(req, resolver{}, 1)
	if err != nil {
		return Page{}, err // *query.InvalidFilterError -> 400 at the controller
	}

	conds := []string{liveOnly}
	if built.Where != "" {
		conds = append(conds, built.Where)
	}
	if built.Keyset != "" {
		conds = append(conds, built.Keyset)
	}
	args := append([]any{}, built.Args...)

	// Filters continue placeholder numbering where query.Build stopped.
	frags, fargs := f.clauses(len(args) + 1)
	conds = append(conds, frags...)
	args = append(args, fargs...)

	sql := accountSelect + ` WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY ` + built.OrderBy +
		` LIMIT ` + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search accounts: %w", err)
	}
	defer rows.Close()

	out := []*Account{}
	for rows.Next() {
		acct, err := scanAccount(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, acct)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate accounts: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, built.Sort.Field))
	}
	return page, nil
}

// sortValue returns the account's value for the effective sort field, so the
// cursor carries the same value the ORDER BY sorted on.
func sortValue(a *Account, field string) any {
	switch field {
	case "code":
		return a.Code
	case "updated_at":
		return a.UpdatedAt
	default: // created_at is the engine's default sort
		return a.CreatedAt
	}
}

// Categories returns the fixed reference tree: 9 categories and 17
// sub-categories, both in sort order. Neither is user-editable (AD-1).
func Categories(ctx context.Context, pool *pgxpool.Pool) ([]Category, []SubCategory, error) {
	catRows, err := pool.Query(ctx, `
		SELECT category_id, category_code, category_name, category_range_low,
		       category_range_high, category_normal_balance, category_sort_order
		FROM lkp_coa_category ORDER BY category_sort_order, category_code`)
	if err != nil {
		return nil, nil, fmt.Errorf("list categories: %w", err)
	}
	defer catRows.Close()

	var cats []Category
	for catRows.Next() {
		var c Category
		if err := catRows.Scan(&c.ID, &c.Code, &c.Name, &c.RangeLow,
			&c.RangeHigh, &c.NormalBalance, &c.SortOrder); err != nil {
			return nil, nil, fmt.Errorf("scan category: %w", err)
		}
		cats = append(cats, c)
	}
	if err := catRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate categories: %w", err)
	}

	subRows, err := pool.Query(ctx, `
		SELECT s.subcategory_id, s.category_id, c.category_code, s.subcategory_code,
		       s.subcategory_name, s.subcategory_range_low, s.subcategory_range_high,
		       s.subcategory_sort_order
		FROM lkp_coa_subcategory s
		JOIN lkp_coa_category c ON c.category_id = s.category_id
		ORDER BY c.category_sort_order, s.subcategory_sort_order`)
	if err != nil {
		return nil, nil, fmt.Errorf("list sub-categories: %w", err)
	}
	defer subRows.Close()

	var subs []SubCategory
	for subRows.Next() {
		var s SubCategory
		if err := subRows.Scan(&s.ID, &s.CategoryID, &s.CategoryCode, &s.Code,
			&s.Name, &s.RangeLow, &s.RangeHigh, &s.SortOrder); err != nil {
			return nil, nil, fmt.Errorf("scan sub-category: %w", err)
		}
		subs = append(subs, s)
	}
	if err := subRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate sub-categories: %w", err)
	}
	return cats, subs, nil
}
