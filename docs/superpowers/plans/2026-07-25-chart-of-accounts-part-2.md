# Chart of Accounts Implementation Plan — Part 2 (Tasks 9–17)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Continuation of** `docs/superpowers/plans/2026-07-25-chart-of-accounts.md`. Tasks 1–8 (schema + seed, RBAC catalog, types/errors, attribute validation, BS/PNL derivation, code allocation, masking) must be complete before starting Task 9.

**Goal:** The store layer, tree assembly, HTTP surface, and database-backed verification for the Chart of Accounts module.

**Spec:** `docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md`. Architecture decisions referenced as AD-1 … AD-11.

## Global Constraints

Identical to Part 1 — restated because a task's implementer may read only this file:

- Database-per-tenant. **No `tenant_id` column anywhere.** The connection is the scope.
- Migrations idempotent and append-only. **Never `ALTER TABLE` to add a column. Never a down-migration.**
- Every route behind JWT + `TenantResolver` + `authz.Check`. Denials call `logSecurityEvent(r, "permission_denied", ...)`.
- All responses JSON carrying `success`, and `message` on failure.
- `*query.InvalidFilterError` maps to **400**, never 500.
- Errors always wrapped: `fmt.Errorf("context: %w", err)`. No `panic()` in production paths.
- `context.Context` first parameter of every store function.
- Files **under 300 lines**.
- Table-driven `testify` tests for every pure function.
- No `company_id`, `subsidiary_id`, or per-account `currency_id`.
- Account **codes** unique among live rows; account **names deliberately not unique** (AD-3).
- Bank account numbers: encrypted at rest, **last-4 only** on read, **no unmask path** (AD-10).

## Interfaces produced by Part 1

Task 9 onward may rely on these existing exactly as written:

```go
// types.go
type Account struct { ID, Code, Name, Description string; SubCategoryID, SubCategoryCode int
                      SubCategoryName string; CategoryCode int; CategoryName string
                      ParentID *string; Depth int; BSPNL, Type string
                      Attributes map[string]any
                      IsPostable, IsActive, IsVisible, IsSystem bool
                      RecordVersion int; CreatedAt, UpdatedAt time.Time }
type Category struct { ID, Code int; Name string; RangeLow, RangeHigh int
                       NormalBalance string; SortOrder int }
type SubCategory struct { ID, CategoryID, CategoryCode, Code int; Name string
                          RangeLow, RangeHigh, SortOrder int }
type DefaultSlot struct { Key, Label, Description string; AccountID *string
                          AccountCode, AccountName string; IsSystem bool
                          SortOrder int; UpdatedAt time.Time }
type CreateInput struct { Name, Description string; SubCategoryID int; ParentID, BSPNL, Type string
                          Attributes map[string]any; IsPostable *bool }
type UpdateInput struct { Name, Description, Type *string; Attributes map[string]any
                          IsPostable, IsActive, IsVisible *bool; RecordVersion int }
type BulkInput struct { UUIDs []string; IsActive, IsVisible *bool }
type BulkResult struct { UUID string; OK bool; Message string }
type HistoryEntry struct { ID int; AccountID *string; SlotKey, Action, Field, OldValue, NewValue string
                           At time.Time; By *int }

// errors.go
var ErrNotFound, ErrCipherUnavailable error
type ClientError struct{ Msg string }
type ConflictError struct{ Msg string; BlockingSlots []string }
func IsClientError(err error) bool
func IsConflict(err error) (ConflictError, bool)
func isUniqueViolation(err error) bool   // unexported

// attributes.go
const BankAccountNumberKey = "accountNumber"
func ValidateAttributes(accountType string, attrs map[string]any) (map[string]any, error)
func ValidAccountTypes() []string

// bspnl.go
const MixedSubCategoryCode = 9100
const BalanceSheet = "BS"; const ProfitAndLoss = "PNL"
func DeriveBSPNL(subCategoryCode int, supplied string) (string, error)

// numbering.go
const MaxChildSuffix = 99
func NextChildCode(parentCode string, taken []string) (string, error)
func NextTopLevelCode(rangeLow, rangeHigh int, taken []string) (string, error)

// masking.go
func Last4(s string) string
func EncryptAttributes(c *secret.Cipher, attrs map[string]any) (map[string]any, error)
func MaskAttributes(attrs map[string]any) map[string]any
```

---

## Task 9: Field resolver and store foundation

**Files:**
- Create: `chartofaccounts/resolver.go`
- Create: `chartofaccounts/store.go`
- Test: `chartofaccounts/resolver_test.go`

**Interfaces:**
- Consumes: `Account` (Task 4), `MaskAttributes` (Task 8), `query.FieldResolver`/`SortResolver`/`SearchResolver`
- Produces: `resolver` (unexported), `accountSelect` const, `scanAccount(row pgx.Row) (*Account, error)`, `nullableInt(v int) any`, `takenCodes(ctx, q, ...) ([]string, error)`, `accountIDByUUID(ctx, q, uuid string) (int, error)`, `rowQuerier` interface. Tasks 10–15 all use these.

- [ ] **Step 1: Write the failing resolver test**

Create `chartofaccounts/resolver_test.go`:

```go
package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/query"
)

func TestResolverResolve(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantOK  bool
		wantDT  query.DataType
	}{
		{"code", "code", true, query.TypeString},
		{"name", "name", true, query.TypeString},
		{"type", "type", true, query.TypeEnum},
		{"bs_pnl", "bs_pnl", true, query.TypeEnum},
		{"is_active", "is_active", true, query.TypeBool},
		{"is_visible", "is_visible", true, query.TypeBool},
		{"is_postable", "is_postable", true, query.TypeBool},
		{"is_system", "is_system", true, query.TypeBool},
		{"subcategory_code", "subcategory_code", true, query.TypeNumber},
		{"category_code", "category_code", true, query.TypeNumber},
		{"created_at", "created_at", true, query.TypeDate},
		{"updated_at", "updated_at", true, query.TypeDate},
		{"unknown key is rejected", "balance", false, ""},
		{"sql injection attempt is rejected", "name; DROP TABLE coa_account", false, ""},
		{"custom field prefix is rejected", "cf:budget", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, dt, ok := resolver{}.Resolve(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.NotEmpty(t, expr)
				assert.Equal(t, tt.wantDT, dt)
			}
		})
	}
}

// AD-9: CoA accounts have NO user-defined custom fields, so unlike the
// inventory resolver there is no "cf:" escape hatch into the JSONB column.
func TestResolverRejectsCustomFieldAccess(t *testing.T) {
	for _, key := range []string{"cf:x", "attributes", "attributes->>'accountNumber'"} {
		_, _, ok := resolver{}.Resolve(key)
		assert.False(t, ok, "key %q must not resolve", key)
	}
}

func TestResolverSortExpr(t *testing.T) {
	// coa_account_code is the module's record_number equivalent: stable,
	// NOT NULL, unique among live rows, so keyset pagination stays correct.
	expr, dt, ok := resolver{}.SortExpr("code")
	assert.True(t, ok)
	assert.Equal(t, "coa_account_code", expr)
	assert.Equal(t, query.TypeString, dt)

	_, _, ok = resolver{}.SortExpr("attributes")
	assert.False(t, ok)
}

func TestResolverInterfaces(t *testing.T) {
	var _ query.FieldResolver = resolver{}
	var _ query.SortResolver = resolver{}
	var _ query.SearchResolver = resolver{}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./chartofaccounts/ -run TestResolver -v
```

Expected: FAIL — `undefined: resolver`.

- [ ] **Step 3: Write `resolver.go`**

```go
package chartofaccounts

import "stonesuite-backend/query"

// resolver implements query.FieldResolver (+ SortResolver + SearchResolver)
// for coa_account. Search joins lkp_coa_subcategory as s and lkp_coa_category
// as c, so those columns are referenced with their aliases.
//
// Note there is NO "cf:" escape hatch into the attributes JSONB, unlike the
// inventory resolver: CoA accounts have no user-defined custom fields (AD-9),
// and the column holds encrypted material that must never be filterable.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":               {"a.coa_account_uuid::text", query.TypeString},
	"code":             {"a.coa_account_code", query.TypeString},
	"name":             {"a.coa_account_name", query.TypeString},
	"description":      {"a.coa_account_description", query.TypeString},
	"type":             {"a.coa_account_type", query.TypeEnum},
	"bs_pnl":           {"a.coa_account_bs_pnl", query.TypeEnum},
	"is_postable":      {"a.coa_account_is_postable", query.TypeBool},
	"is_active":        {"a.coa_account_is_active", query.TypeBool},
	"is_visible":       {"a.coa_account_is_visible", query.TypeBool},
	"is_system":        {"a.coa_account_is_system", query.TypeBool},
	"depth":            {"a.coa_account_depth", query.TypeNumber},
	"subcategory_code": {"s.subcategory_code", query.TypeNumber},
	"subcategory_name": {"s.subcategory_name", query.TypeString},
	"category_code":    {"c.category_code", query.TypeNumber},
	"category_name":    {"c.category_name", query.TypeString},
	"created_at":       {"a.coa_account_created_at", query.TypeDate},
	"updated_at":       {"a.coa_account_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// sortableFields declares stable NOT NULL columns clients may sort by beyond
// the built-in created_at/updated_at.
//
// "code" is this module's record_number equivalent. CLAUDE.md restricts sorts
// to created_at/updated_at/record_number; coa_account has no record_number,
// and coa_account_code satisfies the same underlying requirement -- stable,
// NOT NULL, and unique among live rows -- so keyset cursors stay correct.
// query.SortResolver is the supported extension point for exactly this.
var sortableFields = map[string]resolved{
	"code": {"coa_account_code", query.TypeString},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the account picker's global-search box: code or name
// contains the term.
func (resolver) SearchPredicate(ph string) string {
	return "(a.coa_account_code ILIKE '%'||" + ph + "||'%' OR a.coa_account_name ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}
```

- [ ] **Step 4: Write `store.go`**

```go
package chartofaccounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rowQuerier is the subset of pgx shared by *pgxpool.Pool and pgx.Tx, so store
// helpers work identically inside and outside a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error)
}

// pgconnCommandTag aliases the pgconn tag type so rowQuerier does not force an
// extra import on every consumer.
type pgconnCommandTag = pgconn.CommandTag

// accountColumns is the shared projection. Every read path selects exactly
// these, in this order, so scanAccount is the single scanner.
const accountColumns = `
	a.coa_account_uuid, a.coa_account_code, a.coa_account_name, a.coa_account_description,
	a.subcategory_id, s.subcategory_code, s.subcategory_name, c.category_code, c.category_name,
	p.coa_account_uuid, a.coa_account_depth, a.coa_account_bs_pnl, a.coa_account_type,
	a.coa_account_attributes, a.coa_account_is_postable, a.coa_account_is_active,
	a.coa_account_is_visible, a.coa_account_is_system, a.coa_account_record_version,
	a.coa_account_created_at, a.coa_account_updated_at`

// accountFrom is the shared FROM/JOIN chain. The parent join is LEFT so
// top-level accounts (parent_id NULL) still return a row.
const accountFrom = `
	FROM coa_account a
	JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
	JOIN lkp_coa_category    c ON c.category_id    = s.category_id
	LEFT JOIN coa_account    p ON p.coa_account_id = a.parent_id`

// accountSelect is the full read projection over live rows.
const accountSelect = `SELECT ` + accountColumns + accountFrom

// liveOnly is the soft-delete predicate every read path ANDs in.
const liveOnly = ` a.coa_account_deleted_at IS NULL `

// scanAttributes is the JSONB destination type for coa_account_attributes.
type scanAttributes = map[string]any

// scanAccount reads one row of accountColumns. Attributes are masked here, at
// the single point every read passes through, so encrypted material cannot
// escape the store layer by way of a path that forgot to mask (AD-10).
func scanAccount(row pgx.Row) (*Account, error) {
	var (
		a          Account
		parentUUID *string
		attrs      scanAttributes
	)
	if err := row.Scan(
		&a.ID, &a.Code, &a.Name, &a.Description,
		&a.SubCategoryID, &a.SubCategoryCode, &a.SubCategoryName, &a.CategoryCode, &a.CategoryName,
		&parentUUID, &a.Depth, &a.BSPNL, &a.Type,
		&attrs, &a.IsPostable, &a.IsActive,
		&a.IsVisible, &a.IsSystem, &a.RecordVersion,
		&a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	a.ParentID = parentUUID
	a.Attributes = MaskAttributes(attrs)
	return &a, nil
}

// nullableInt converts a non-positive employee id to SQL NULL, matching the
// convention in crmstore and inventory (employee id 0/unresolved => NULL).
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// accountIDByUUID resolves a public uuid to the internal serial id, returning
// ErrNotFound when the uuid matches nothing live.
func accountIDByUUID(ctx context.Context, q rowQuerier, uuid string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT coa_account_id FROM coa_account
		 WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve account uuid: %w", err)
	}
	return id, nil
}

// takenCodes returns every live account code, for the numbering allocator.
// The whole set is read rather than a filtered slice because both allocators
// need to see child codes and top-level codes together to reuse gaps
// correctly, and 127 seeded rows plus user additions is a trivially small set.
func takenCodes(ctx context.Context, q rowQuerier) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT coa_account_code FROM coa_account WHERE coa_account_deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list taken codes: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("scan taken code: %w", err)
		}
		out = append(out, code)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate taken codes: %w", err)
	}
	return out, nil
}

// ensurePool is a compile-time assertion that *pgxpool.Pool satisfies rowQuerier.
var _ rowQuerier = (*pgxpool.Pool)(nil)
```

- [ ] **Step 5: Add the missing pgconn import**

`store.go` references `pgconn.CommandTag`. Add to its import block:

```go
	"github.com/jackc/pgx/v5/pgconn"
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go build ./chartofaccounts/ && go test ./chartofaccounts/ -run TestResolver -v
```

Expected: build succeeds; all `TestResolver*` subtests PASS.

- [ ] **Step 7: Commit**

```bash
git add chartofaccounts/resolver.go chartofaccounts/store.go chartofaccounts/resolver_test.go
git commit -m "feat(coa): add field resolver whitelist and store foundation"
```

---

## Task 10: Read paths — Get, Search, Categories

**Files:**
- Create: `chartofaccounts/store_get.go`

**Interfaces:**
- Consumes: `accountSelect`, `accountFrom`, `liveOnly`, `scanAccount`, `resolver` (Task 9); `query.Build`, `query.Request`
- Produces: `Get(ctx, pool, uuid) (*Account, error)`, `Search(ctx, pool, req, f Filters) (Page, error)`, `Categories(ctx, pool) ([]Category, []SubCategory, error)`, `Page` struct, `Filters` struct. Task 11 (`tree`) and Task 16 (controllers) consume these.

- [ ] **Step 1: Write `store_get.go`**

```go
package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
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
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request, f Filters) (Page, error) {
	// Placeholder 1 is reserved for nothing here; query.Build starts at 1 and
	// Filters continue from wherever it stops.
	built, err := query.Build(req, resolver{}, 1)
	if err != nil {
		return Page{}, err // *query.InvalidFilterError -> 400 at the controller
	}

	conds := []string{liveOnly}
	args := append([]any{}, built.Args...)
	if built.Where != "" {
		conds = append(conds, built.Where)
	}
	frags, fargs := f.clauses(len(args) + 1)
	conds = append(conds, frags...)
	args = append(args, fargs...)

	sql := accountSelect + ` WHERE ` + strings.Join(conds, " AND ") + ` ` + built.OrderLimit

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search accounts: %w", err)
	}
	defer rows.Close()

	var out []*Account
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
	if len(out) > built.Limit {
		page.Records = out[:built.Limit]
		page.HasMore = true
		last := page.Records[len(page.Records)-1]
		page.NextCursor = query.EncodeCursor(built.SortField, last.Code, last.ID)
	}
	return page, nil
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
```

- [ ] **Step 2: Reconcile with the real `query` API**

`query.Build` returns a `query.Built`. Before assuming the field names used above (`Where`, `Args`, `OrderLimit`, `Limit`, `SortField`) and the helper `query.EncodeCursor`, read the actual definitions:

```bash
sed -n '1,60p' query/builder.go && grep -n "type Built\|func EncodeCursor\|func encodeCursor" query/*.go
```

Adjust the `Search` body to match the real field names and cursor helper. **Do not change `query/`** — it is shared by every module. If cursor encoding is unexported, copy the pattern used by `inventory.Search`:

```bash
sed -n '/^func Search/,/^}/p' inventory/store.go
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./chartofaccounts/ && go vet ./chartofaccounts/
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/store_get.go
git commit -m "feat(coa): add account get, search and category read paths"
```

---

## Task 11: Tree assembly

**Files:**
- Create: `chartofaccounts/tree.go`
- Test: `chartofaccounts/tree_test.go`

**Interfaces:**
- Consumes: `Account`, `Category`, `SubCategory` (Task 4)
- Produces: `BuildTree(cats []Category, subs []SubCategory, accts []*Account, opts TreeOptions) []TreeSection`, `TreeOptions`, `TreeSection`, `TreeCategory`, `TreeSubCategory`, `TreeAccount`. Task 16's tree handler calls `BuildTree`.

- [ ] **Step 1: Write the failing test**

Create `chartofaccounts/tree_test.go`:

```go
package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func treeFixture() ([]Category, []SubCategory, []*Account) {
	cats := []Category{
		{ID: 1, Code: 1000, Name: "Assets", NormalBalance: "debit", SortOrder: 1},
		{ID: 4, Code: 4000, Name: "Revenue", NormalBalance: "credit", SortOrder: 4},
	}
	subs := []SubCategory{
		{ID: 1, CategoryID: 1, CategoryCode: 1000, Code: 1100, Name: "Current Assets", SortOrder: 1},
		{ID: 2, CategoryID: 1, CategoryCode: 1000, Code: 1200, Name: "Fixed Assets", SortOrder: 2},
		{ID: 7, CategoryID: 4, CategoryCode: 4000, Code: 4100, Name: "Sales", SortOrder: 1},
	}
	parent := "uuid-1103"
	accts := []*Account{
		{ID: "uuid-1103", Code: "1103", Name: "Bank Account - Operating", SubCategoryID: 1,
			SubCategoryCode: 1100, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1103-01", Code: "1103.01", Name: "HDFC USA", SubCategoryID: 1,
			SubCategoryCode: 1100, CategoryCode: 1000, BSPNL: "BS", Depth: 1,
			ParentID: &parent, IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1101", Code: "1101", Name: "Cash on Hand", SubCategoryID: 1,
			SubCategoryCode: 1100, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1201", Code: "1201", Name: "Land", SubCategoryID: 2,
			SubCategoryCode: 1200, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: false, IsVisible: true, IsPostable: true},
		{ID: "uuid-4101", Code: "4101", Name: "Product Sales", SubCategoryID: 7,
			SubCategoryCode: 4100, CategoryCode: 4000, BSPNL: "PNL", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
	}
	return cats, subs, accts
}

func TestBuildTreeGroupsBySection(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})

	require.Len(t, got, 2)
	assert.Equal(t, "BS", got[0].BSPNL)
	assert.Equal(t, "PNL", got[1].BSPNL)

	require.Len(t, got[0].Categories, 1)
	assert.Equal(t, 1000, got[0].Categories[0].Code)
	require.Len(t, got[0].Categories[0].SubCategories, 2)
	assert.Equal(t, 1100, got[0].Categories[0].SubCategories[0].Code)
	assert.Equal(t, 1200, got[0].Categories[0].SubCategories[1].Code)
}

func TestBuildTreeOrdersAccountsByCode(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})

	current := got[0].Categories[0].SubCategories[0].Accounts
	require.Len(t, current, 2, "1101 and 1103; 1103.01 nests under 1103")
	assert.Equal(t, "1101", current[0].Code)
	assert.Equal(t, "1103", current[1].Code)
}

func TestBuildTreeNestsChildren(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})

	bank := got[0].Categories[0].SubCategories[0].Accounts[1]
	require.Len(t, bank.Children, 1)
	assert.Equal(t, "1103.01", bank.Children[0].Code)
	assert.Equal(t, "HDFC USA", bank.Children[0].Name)
}

func TestBuildTreeExcludesInactiveByDefault(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{})

	fixed := got[0].Categories[0].SubCategories[1]
	assert.Equal(t, 1200, fixed.Code)
	assert.Empty(t, fixed.Accounts, "1201 Land is inactive")
}

func TestBuildTreeExcludesHidden(t *testing.T) {
	cats, subs, accts := treeFixture()
	accts[2].IsVisible = false // 1101 Cash on Hand
	accts[2].IsActive = false  // active implies visible, so retire it too

	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})
	codes := []string{}
	for _, a := range got[0].Categories[0].SubCategories[0].Accounts {
		codes = append(codes, a.Code)
	}
	assert.NotContains(t, codes, "1101")

	got = BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true, IncludeHidden: true})
	codes = []string{}
	for _, a := range got[0].Categories[0].SubCategories[0].Accounts {
		codes = append(codes, a.Code)
	}
	assert.Contains(t, codes, "1101")
}

// A child whose parent was filtered out must still appear rather than vanish
// silently -- an account missing from a financial report is worse than one
// shown at the wrong indent.
func TestBuildTreePromotesOrphans(t *testing.T) {
	cats, subs, accts := treeFixture()
	accts[0].IsActive = false // 1103, the parent
	accts[0].IsVisible = false

	got := BuildTree(cats, subs, accts, TreeOptions{})
	codes := []string{}
	for _, a := range got[0].Categories[0].SubCategories[0].Accounts {
		codes = append(codes, a.Code)
	}
	assert.Contains(t, codes, "1103.01", "orphaned child must be promoted, not dropped")
}

func TestBuildTreeEmptyInputs(t *testing.T) {
	got := BuildTree(nil, nil, nil, TreeOptions{})
	assert.Empty(t, got)
}

func TestBuildTreeKeepsEmptySubCategories(t *testing.T) {
	cats, subs, _ := treeFixture()
	got := BuildTree(cats, subs, nil, TreeOptions{})
	require.Len(t, got, 2)
	assert.Len(t, got[0].Categories[0].SubCategories, 2,
		"structure is shown even when no accounts fall under it")
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./chartofaccounts/ -run TestBuildTree -v
```

Expected: FAIL — `undefined: BuildTree`.

- [ ] **Step 3: Write `tree.go`**

```go
package chartofaccounts

import "sort"

// TreeOptions toggles what the report includes. The zero value is the default
// view: active, visible accounts only.
type TreeOptions struct {
	IncludeInactive bool
	IncludeHidden   bool
}

// TreeAccount is one account in the report, with its children nested.
type TreeAccount struct {
	*Account
	Children []*TreeAccount `json:"children"`
}

// TreeSubCategory groups accounts under a fixed sub-category.
type TreeSubCategory struct {
	ID       int            `json:"id"`
	Code     int            `json:"code"`
	Name     string         `json:"name"`
	Accounts []*TreeAccount `json:"accounts"`
}

// TreeCategory groups sub-categories under a fixed category.
type TreeCategory struct {
	ID            int                `json:"id"`
	Code          int                `json:"code"`
	Name          string             `json:"name"`
	NormalBalance string             `json:"normalBalance"`
	SubCategories []*TreeSubCategory `json:"subCategories"`
}

// TreeSection is the top split: balance sheet vs profit and loss.
type TreeSection struct {
	BSPNL      string          `json:"bsPnl"`
	Label      string          `json:"label"`
	Categories []*TreeCategory `json:"categories"`
}

// sectionLabels renders the two BS/PNL markers for display.
var sectionLabels = map[string]string{
	BalanceSheet:  "Balance Sheet",
	ProfitAndLoss: "Profit & Loss",
}

// BuildTree assembles flat rows into the report structure:
//
//	BS/PNL -> category -> sub-category -> account -> children
//
// Sub-categories are kept even when empty, so the report shows the tenant's
// full account structure rather than only the parts currently populated.
//
// A section appears only when at least one of its categories does. An account
// whose parent was filtered out is promoted to top level rather than dropped:
// silently omitting an account from a financial report is worse than showing
// it at the wrong indent.
func BuildTree(cats []Category, subs []SubCategory, accts []*Account, opts TreeOptions) []TreeSection {
	visible := filterAccounts(accts, opts)

	// Index children by parent uuid, keeping only parents that survived.
	present := make(map[string]bool, len(visible))
	for _, a := range visible {
		present[a.ID] = true
	}
	childrenOf := make(map[string][]*Account)
	var roots []*Account
	for _, a := range visible {
		if a.ParentID != nil && present[*a.ParentID] {
			childrenOf[*a.ParentID] = append(childrenOf[*a.ParentID], a)
			continue
		}
		roots = append(roots, a) // top-level, or an orphan we promote
	}

	rootsBySub := make(map[int][]*Account, len(subs))
	for _, a := range roots {
		rootsBySub[a.SubCategoryID] = append(rootsBySub[a.SubCategoryID], a)
	}

	subsByCategory := make(map[int][]SubCategory, len(cats))
	for _, s := range subs {
		subsByCategory[s.CategoryID] = append(subsByCategory[s.CategoryID], s)
	}

	byBSPNL := map[string][]*TreeCategory{}
	for _, c := range cats {
		grouped := map[string]*TreeCategory{}
		for _, s := range subsByCategory[c.ID] {
			ts := &TreeSubCategory{ID: s.ID, Code: s.Code, Name: s.Name}
			for _, a := range sortByCode(rootsBySub[s.ID]) {
				ts.Accounts = append(ts.Accounts, &TreeAccount{
					Account:  a,
					Children: wrap(sortByCode(childrenOf[a.ID])),
				})
			}
			// A sub-category's side follows its accounts; with none, fall back
			// to the category's own side via any seeded account's derivation.
			side := sideOf(s.Code, ts.Accounts)
			tc, ok := grouped[side]
			if !ok {
				tc = &TreeCategory{ID: c.ID, Code: c.Code, Name: c.Name, NormalBalance: c.NormalBalance}
				grouped[side] = tc
			}
			tc.SubCategories = append(tc.SubCategories, ts)
		}
		for side, tc := range grouped {
			byBSPNL[side] = append(byBSPNL[side], tc)
		}
	}

	var out []TreeSection
	for _, side := range []string{BalanceSheet, ProfitAndLoss} {
		tcs := byBSPNL[side]
		if len(tcs) == 0 {
			continue
		}
		sort.SliceStable(tcs, func(i, j int) bool { return tcs[i].Code < tcs[j].Code })
		out = append(out, TreeSection{BSPNL: side, Label: sectionLabels[side], Categories: tcs})
	}
	return out
}

// filterAccounts applies the visibility toggles.
func filterAccounts(accts []*Account, opts TreeOptions) []*Account {
	out := make([]*Account, 0, len(accts))
	for _, a := range accts {
		if !a.IsVisible && !opts.IncludeHidden {
			continue
		}
		if !a.IsActive && !opts.IncludeInactive {
			continue
		}
		out = append(out, a)
	}
	return out
}

// sortByCode orders accounts by code. Codes are zero-padded ("1103.09" before
// "1103.10"), so lexical order matches numeric order.
func sortByCode(in []*Account) []*Account {
	out := append([]*Account(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// wrap lifts plain accounts into leaf tree nodes. The tree is capped at two
// levels (AD-4), so a child never has children of its own.
func wrap(in []*Account) []*TreeAccount {
	out := make([]*TreeAccount, 0, len(in))
	for _, a := range in {
		out = append(out, &TreeAccount{Account: a, Children: nil})
	}
	return out
}

// sideOf reports which section a sub-category belongs to. Sub-category 9100
// mixes BS and PNL (AD-2), so its side is taken from the accounts actually
// present; every other sub-category has a fixed side.
func sideOf(subCategoryCode int, accts []*TreeAccount) string {
	if side, err := DeriveBSPNL(subCategoryCode, ""); err == nil {
		return side
	}
	if len(accts) > 0 {
		return accts[0].BSPNL
	}
	return BalanceSheet
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./chartofaccounts/ -run TestBuildTree -v
```

Expected: PASS — all 8 tree tests.

**If `TestBuildTreeGroupsBySection` fails on sub-category 9100 splitting a category across both sections:** that is intended behaviour, not a bug — 9000 System & Control legitimately appears under both Balance Sheet and Profit & Loss because its accounts do. Adjust the fixture expectation, not `sideOf`.

- [ ] **Step 5: Commit**

```bash
git add chartofaccounts/tree.go chartofaccounts/tree_test.go
git commit -m "feat(coa): assemble the grouped account report tree"
```

---

## Remaining tasks

Tasks 12–17 are specified in:

`docs/superpowers/plans/2026-07-25-chart-of-accounts-part-3.md`

| Task | Deliverable |
|---|---|
| 12 | `store_create.go` — allocation, validation, encryption, history |
| 13 | `store_guard.go` + `store_update.go` — the AD-7 guard and its first caller |
| 14 | `store_delete.go` + `store_bulk.go` — the guard's other three callers |
| 15 | `store_defaults.go` + `store_history.go` — slots and audit trail |
| 16 | `controllers/*` + `main.go` — 12 routes behind the full security chain |
| 17 | `-tags dbtest` suite — seed counts, idempotency, every constraint |
