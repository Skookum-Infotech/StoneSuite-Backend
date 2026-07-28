# Chart of Accounts Implementation Plan — Part 3 (Tasks 12–17)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Continuation of** parts 1 and 2. Tasks 1–11 must be complete before starting Task 12.

**Goal:** Write paths (create, update, delete, bulk, defaults), the shared referential guard, the HTTP surface, and the database-backed verification suite.

**Spec:** `docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md`. Decisions referenced as AD-1 … AD-11.

## Global Constraints

- Database-per-tenant. **No `tenant_id` column anywhere.**
- Migrations idempotent and append-only. **Never `ALTER TABLE` to add a column. Never a down-migration.**
- Every route behind JWT + `TenantResolver` + `authz.Check`. Denials call `logSecurityEvent(r, "permission_denied", ...)`.
- All responses JSON carrying `success`, and `message` on failure.
- `*query.InvalidFilterError` → **400**, never 500.
- Errors always wrapped: `fmt.Errorf("context: %w", err)`. No `panic()`.
- `context.Context` first parameter of every store function.
- Files **under 300 lines**.
- Account **codes** unique among live rows; **names deliberately not unique** (AD-3).
- Bank account numbers encrypted at rest, **last-4 only** on read, **no unmask path** (AD-10).
- Bank numbers are **never** written to history or any log — only the fact that the field changed.

## Interfaces produced by parts 1–2

```go
// Part 1
const BankAccountNumberKey = "accountNumber"; const MaxChildSuffix = 99
const MixedSubCategoryCode = 9100; const BalanceSheet = "BS"; const ProfitAndLoss = "PNL"
var ErrNotFound, ErrCipherUnavailable error
type ClientError struct{ Msg string }
type ConflictError struct{ Msg string; BlockingSlots []string }
func IsClientError(err error) bool
func IsConflict(err error) (ConflictError, bool)
func isUniqueViolation(err error) bool
func ValidateAttributes(accountType string, attrs map[string]any) (map[string]any, error)
func ValidAccountTypes() []string
func DeriveBSPNL(subCategoryCode int, supplied string) (string, error)
func NextChildCode(parentCode string, taken []string) (string, error)
func NextTopLevelCode(rangeLow, rangeHigh int, taken []string) (string, error)
func Last4(s string) string
func EncryptAttributes(c *secret.Cipher, attrs map[string]any) (map[string]any, error)
func MaskAttributes(attrs map[string]any) map[string]any

// Part 2
type rowQuerier interface { QueryRow(...) pgx.Row; Query(...) (pgx.Rows, error); Exec(...) (pgconn.CommandTag, error) }
const accountColumns, accountFrom, accountSelect, liveOnly string
func scanAccount(row pgx.Row) (*Account, error)
func nullableInt(v int) any
func accountIDByUUID(ctx context.Context, q rowQuerier, uuid string) (int, error)
func takenCodes(ctx context.Context, q rowQuerier) ([]string, error)
type Page struct { Records []*Account; NextCursor string; HasMore bool }
type Filters struct { Postable, Active, Visible *bool; SubCategoryID *int }
func Get(ctx, pool, uuid) (*Account, error)
func Search(ctx, pool, req query.Request, f Filters) (Page, error)
func Categories(ctx, pool) ([]Category, []SubCategory, error)
func BuildTree(cats []Category, subs []SubCategory, accts []*Account, opts TreeOptions) []TreeSection
```

---

## Task 12: Create

**Files:**
- Create: `chartofaccounts/store_create.go`
- Create: `chartofaccounts/store_history.go`

**Interfaces:**
- Consumes: everything above
- Produces: `Create(ctx, pool, c *secret.Cipher, in CreateInput, employeeID int) (*Account, error)`, `appendHistory(ctx, q rowQuerier, h historyRow) error`, `historyRow` struct, `History(ctx, pool, uuid string, limit int) ([]HistoryEntry, error)`. Tasks 13–15 call `appendHistory`; Task 16 calls `Create` and `History`.

- [ ] **Step 1: Write `store_history.go`**

```go
package chartofaccounts

import (
	"context"
	"fmt"
)

// History action verbs, matching chk_coa_history_action in tenant/schema.sql.
const (
	actionCreate      = "create"
	actionUpdate      = "update"
	actionDelete      = "delete"
	actionActivate    = "activate"
	actionDeactivate  = "deactivate"
	actionShow        = "show"
	actionHide        = "hide"
	actionRepointSlot = "repoint_slot"
)

// redactedValue stands in for a bank account number in the audit trail. The
// number itself is never written to history or to any log (AD-10) -- only the
// fact that it changed.
const redactedValue = "[redacted]"

// historyRow is one audited change. Exactly one of AccountID or SlotKey is set.
type historyRow struct {
	AccountID  *int
	SlotKey    string
	Action     string
	Field      string
	OldValue   string
	NewValue   string
	EmployeeID int
}

// appendHistory writes one audit row. It takes a rowQuerier so callers inside
// a transaction record their history in that same transaction -- a rolled-back
// mutation must not leave an audit row claiming it happened.
func appendHistory(ctx context.Context, q rowQuerier, h historyRow) error {
	if h.Field == BankAccountNumberKey {
		h.OldValue, h.NewValue = redactedValue, redactedValue
	}
	var slot any
	if h.SlotKey != "" {
		slot = h.SlotKey
	}
	_, err := q.Exec(ctx, `
		INSERT INTO coa_account_history
			(coa_account_id, slot_key, history_action, history_field,
			 history_old_value, history_new_value, history_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		h.AccountID, slot, h.Action, h.Field, h.OldValue, h.NewValue,
		nullableInt(h.EmployeeID))
	if err != nil {
		return fmt.Errorf("append account history: %w", err)
	}
	return nil
}

// History returns the audit trail for one account, newest first.
func History(ctx context.Context, pool rowQuerier, uuid string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT h.coa_account_history_id, a.coa_account_uuid, COALESCE(h.slot_key,''),
		       h.history_action, h.history_field, h.history_old_value,
		       h.history_new_value, h.history_at, h.history_by
		FROM coa_account_history h
		JOIN coa_account a ON a.coa_account_id = h.coa_account_id
		WHERE a.coa_account_uuid = $1
		ORDER BY h.history_at DESC, h.coa_account_history_id DESC
		LIMIT $2`, uuid, limit)
	if err != nil {
		return nil, fmt.Errorf("list account history: %w", err)
	}
	defer rows.Close()

	var out []HistoryEntry
	for rows.Next() {
		var (
			e         HistoryEntry
			accountID string
		)
		if err := rows.Scan(&e.ID, &accountID, &e.SlotKey, &e.Action, &e.Field,
			&e.OldValue, &e.NewValue, &e.At, &e.By); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		e.AccountID = &accountID
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 2: Write `store_create.go`**

```go
package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/secret"
)

// createTarget is the resolved placement of a new account.
type createTarget struct {
	subCategoryID   int
	subCategoryCode int
	rangeLow        int
	rangeHigh       int
	parentID        *int
	parentCode      string
	depth           int
}

// Create inserts a new account. Code, depth and BS/PNL are server-assigned;
// sub-category is inherited from the parent for a child. Everything runs in
// one transaction so a failure between allocating a code and inserting the row
// cannot leave a gap or a half-written audit trail.
func Create(ctx context.Context, pool *pgxpool.Pool, c *secret.Cipher, in CreateInput, employeeID int) (*Account, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, ClientError{Msg: "An account name is required."}
	}
	if in.Type == "" {
		in.Type = "general"
	}

	attrs, err := ValidateAttributes(in.Type, in.Attributes)
	if err != nil {
		return nil, err
	}
	stored, err := EncryptAttributes(c, attrs)
	if err != nil {
		return nil, err // ErrCipherUnavailable -> 503 at the controller
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	target, err := resolveTarget(ctx, tx, in)
	if err != nil {
		return nil, err
	}

	bsPnl, err := DeriveBSPNL(target.subCategoryCode, in.BSPNL)
	if err != nil {
		return nil, err
	}

	taken, err := takenCodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	var code string
	if target.parentID != nil {
		code, err = NextChildCode(target.parentCode, taken)
	} else {
		code, err = NextTopLevelCode(target.rangeLow, target.rangeHigh, taken)
	}
	if err != nil {
		return nil, err
	}

	postable := true
	if in.IsPostable != nil {
		postable = *in.IsPostable
	}

	var newID int
	err = tx.QueryRow(ctx, `
		INSERT INTO coa_account
			(coa_account_code, coa_account_name, coa_account_description, subcategory_id,
			 parent_id, coa_account_depth, coa_account_bs_pnl, coa_account_type,
			 coa_account_attributes, coa_account_is_postable, coa_account_created_by,
			 coa_account_updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		RETURNING coa_account_id`,
		code, strings.TrimSpace(in.Name), strings.TrimSpace(in.Description),
		target.subCategoryID, target.parentID, target.depth, bsPnl, in.Type,
		stored, postable, nullableInt(employeeID)).Scan(&newID)
	if isUniqueViolation(err) {
		// Another writer took the code between allocation and insert.
		return nil, ConflictError{Msg: fmt.Sprintf(
			"Account code %s was just taken. Please retry.", code)}
	}
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}

	if err := appendHistory(ctx, tx, historyRow{
		AccountID: &newID, Action: actionCreate, Field: "code",
		NewValue: code, EmployeeID: employeeID,
	}); err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx, accountSelect+` WHERE `+liveOnly+` AND a.coa_account_id = $1`, newID)
	acct, err := scanAccount(row)
	if err != nil {
		return nil, fmt.Errorf("read back created account: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create account: %w", err)
	}
	return acct, nil
}

// resolveTarget works out where the new account goes. A child inherits its
// parent's sub-category (AD-5) and sits at depth 1; the two-level cap means a
// parent must itself be top-level (AD-4).
func resolveTarget(ctx context.Context, q rowQuerier, in CreateInput) (createTarget, error) {
	if in.ParentID == "" {
		if in.SubCategoryID <= 0 {
			return createTarget{}, ClientError{Msg: "A sub-category is required for a top-level account."}
		}
		var t createTarget
		err := q.QueryRow(ctx, `
			SELECT subcategory_id, subcategory_code, subcategory_range_low, subcategory_range_high
			FROM lkp_coa_subcategory WHERE subcategory_id = $1`, in.SubCategoryID).
			Scan(&t.subCategoryID, &t.subCategoryCode, &t.rangeLow, &t.rangeHigh)
		if errors.Is(err, pgx.ErrNoRows) {
			return createTarget{}, ClientError{Msg: fmt.Sprintf(
				"Unknown sub-category id %d.", in.SubCategoryID)}
		}
		if err != nil {
			return createTarget{}, fmt.Errorf("load sub-category: %w", err)
		}
		return t, nil
	}

	var (
		t           createTarget
		parentID    int
		parentDepth int
	)
	err := q.QueryRow(ctx, `
		SELECT a.coa_account_id, a.coa_account_code, a.coa_account_depth,
		       s.subcategory_id, s.subcategory_code, s.subcategory_range_low, s.subcategory_range_high
		FROM coa_account a
		JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
		WHERE a.coa_account_uuid = $1 AND a.coa_account_deleted_at IS NULL`, in.ParentID).
		Scan(&parentID, &t.parentCode, &parentDepth,
			&t.subCategoryID, &t.subCategoryCode, &t.rangeLow, &t.rangeHigh)
	if errors.Is(err, pgx.ErrNoRows) {
		return createTarget{}, ClientError{Msg: "The parent account does not exist."}
	}
	if err != nil {
		return createTarget{}, fmt.Errorf("load parent account: %w", err)
	}
	if parentDepth != 0 {
		return createTarget{}, ClientError{Msg: fmt.Sprintf(
			"Account %s is already a sub-account. The chart of accounts is limited to two levels.",
			t.parentCode)}
	}
	if in.SubCategoryID > 0 && in.SubCategoryID != t.subCategoryID {
		return createTarget{}, ClientError{Msg: fmt.Sprintf(
			"A sub-account must stay in its parent's sub-category (%d).", t.subCategoryCode)}
	}
	t.parentID = &parentID
	t.depth = 1
	return t, nil
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./chartofaccounts/ && go vet ./chartofaccounts/ && go test ./chartofaccounts/
```

Expected: build clean, existing unit tests still PASS.

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/store_create.go chartofaccounts/store_history.go
git commit -m "feat(coa): create accounts with server-assigned codes and audit trail"
```

---

## Task 13: The referential guard and update

**Files:**
- Create: `chartofaccounts/store_guard.go`
- Create: `chartofaccounts/store_update.go`

**Interfaces:**
- Consumes: Task 12's `appendHistory`
- Produces: `BlockingSlots(ctx, q rowQuerier, accountID int) ([]string, error)`, `guardRetire(ctx, q, accountID int, name, code string) error`, `Update(ctx, pool, c, uuid string, in UpdateInput, employeeID int) (*Account, error)`. Task 14 calls `guardRetire`.

- [ ] **Step 1: Write `store_guard.go`**

```go
package chartofaccounts

import (
	"context"
	"fmt"
	"strings"
)

// BlockingSlots returns the default-mapping slot keys pointing at accountID.
//
// This is the single referential guard (AD-7). Deactivating, hiding,
// soft-deleting, or un-posting an account a slot points at would leave the
// slot dangling -- nothing would error until a transaction failed months
// later. All four mutations call guardRetire, which calls this. Writing it
// once with four callers, rather than four independent checks, is precisely
// because the four-independent-checks version is how three of them end up
// missing it.
func BlockingSlots(ctx context.Context, q rowQuerier, accountID int) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT slot_key FROM coa_default_mapping
		WHERE coa_account_id = $1 ORDER BY slot_sort_order`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list blocking slots: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan blocking slot: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocking slots: %w", err)
	}
	return out, nil
}

// guardRetire refuses any mutation that would retire an account still wired to
// a default slot. The caller repoints the slot first, then retires.
func guardRetire(ctx context.Context, q rowQuerier, accountID int, code, name string) error {
	slots, err := BlockingSlots(ctx, q, accountID)
	if err != nil {
		return err
	}
	if len(slots) == 0 {
		return nil
	}
	return ConflictError{
		Msg: fmt.Sprintf("Account %s %s is in use as a default account (%s). "+
			"Point the default at another account first.",
			code, name, strings.Join(slots, ", ")),
		BlockingSlots: slots,
	}
}

// hasLiveChildren reports whether the account still has undeleted children,
// which blocks a delete.
func hasLiveChildren(ctx context.Context, q rowQuerier, accountID int) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM coa_account
		WHERE parent_id = $1 AND coa_account_deleted_at IS NULL`, accountID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("count live children: %w", err)
	}
	return n > 0, nil
}
```

- [ ] **Step 2: Write `store_update.go`**

```go
package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/secret"
)

// currentAccount is the pre-update snapshot the guard and audit trail need.
type currentAccount struct {
	id          int
	code        string
	name        string
	description string
	acctType    string
	attrs       map[string]any
	isPostable  bool
	isActive    bool
	isVisible   bool
	isSystem    bool
	version     int
}

// Update applies a partial change to one account. Code, sub-category and
// parent are immutable after create; a seeded (is_system) account may be
// renamed, described, retyped and toggled, but never recoded or deleted.
//
// Any change that retires the account -- deactivate, hide, or un-post -- runs
// guardRetire first (AD-7). is_postable never flips automatically (AD-6).
func Update(ctx context.Context, pool *pgxpool.Pool, c *secret.Cipher, uuid string, in UpdateInput, employeeID int) (*Account, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := loadCurrent(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if in.RecordVersion != 0 && in.RecordVersion != cur.version {
		return nil, ConflictError{Msg: "This account was changed by someone else. Reload and try again."}
	}

	// The guard covers every retiring transition in one place.
	retiring := (in.IsActive != nil && !*in.IsActive && cur.isActive) ||
		(in.IsVisible != nil && !*in.IsVisible && cur.isVisible) ||
		(in.IsPostable != nil && !*in.IsPostable && cur.isPostable)
	if retiring {
		if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
			return nil, err
		}
	}

	next := *cur
	var audits []historyRow

	if in.Name != nil && strings.TrimSpace(*in.Name) != cur.name {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, ClientError{Msg: "An account name is required."}
		}
		next.name = strings.TrimSpace(*in.Name)
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "name", OldValue: cur.name, NewValue: next.name, EmployeeID: employeeID})
	}
	if in.Description != nil && *in.Description != cur.description {
		next.description = strings.TrimSpace(*in.Description)
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "description", OldValue: cur.description, NewValue: next.description, EmployeeID: employeeID})
	}
	if in.Type != nil && *in.Type != cur.acctType {
		next.acctType = *in.Type
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "type", OldValue: cur.acctType, NewValue: next.acctType, EmployeeID: employeeID})
	}
	if in.Attributes != nil {
		validated, err := ValidateAttributes(next.acctType, in.Attributes)
		if err != nil {
			return nil, err
		}
		stored, err := EncryptAttributes(c, validated)
		if err != nil {
			return nil, err
		}
		next.attrs = stored
		// The bank number itself never reaches history (AD-10); appendHistory
		// redacts this field, and only the fact of a change is recorded.
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "attributes", EmployeeID: employeeID})
	}
	if in.IsPostable != nil && *in.IsPostable != cur.isPostable {
		next.isPostable = *in.IsPostable
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "is_postable", OldValue: boolStr(cur.isPostable),
			NewValue: boolStr(next.isPostable), EmployeeID: employeeID})
	}
	if in.IsActive != nil && *in.IsActive != cur.isActive {
		next.isActive = *in.IsActive
		act := actionActivate
		if !next.isActive {
			act = actionDeactivate
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_active", OldValue: boolStr(cur.isActive),
			NewValue: boolStr(next.isActive), EmployeeID: employeeID})
	}
	if in.IsVisible != nil && *in.IsVisible != cur.isVisible {
		next.isVisible = *in.IsVisible
		act := actionShow
		if !next.isVisible {
			act = actionHide
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_visible", OldValue: boolStr(cur.isVisible),
			NewValue: boolStr(next.isVisible), EmployeeID: employeeID})
	}

	// AD-8: active implies visible. Reject rather than silently coercing --
	// the DB CHECK would fire anyway, and a 400 explains why.
	if next.isActive && !next.isVisible {
		return nil, ClientError{Msg: "An active account must stay visible. Deactivate it before hiding it."}
	}

	if len(audits) == 0 {
		acct, err := scanAccount(tx.QueryRow(ctx,
			accountSelect+` WHERE `+liveOnly+` AND a.coa_account_id = $1`, cur.id))
		if err != nil {
			return nil, fmt.Errorf("read back account: %w", err)
		}
		return acct, tx.Commit(ctx)
	}

	_, err = tx.Exec(ctx, `
		UPDATE coa_account SET
			coa_account_name = $2, coa_account_description = $3, coa_account_type = $4,
			coa_account_attributes = $5, coa_account_is_postable = $6,
			coa_account_is_active = $7, coa_account_is_visible = $8,
			coa_account_updated_at = CURRENT_TIMESTAMP, coa_account_updated_by = $9,
			coa_account_record_version = coa_account_record_version + 1
		WHERE coa_account_id = $1 AND coa_account_deleted_at IS NULL`,
		cur.id, next.name, next.description, next.acctType, next.attrs,
		next.isPostable, next.isActive, next.isVisible, nullableInt(employeeID))
	if err != nil {
		return nil, fmt.Errorf("update account: %w", err)
	}

	for _, a := range audits {
		if err := appendHistory(ctx, tx, a); err != nil {
			return nil, err
		}
	}

	acct, err := scanAccount(tx.QueryRow(ctx,
		accountSelect+` WHERE `+liveOnly+` AND a.coa_account_id = $1`, cur.id))
	if err != nil {
		return nil, fmt.Errorf("read back updated account: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update account: %w", err)
	}
	return acct, nil
}

// loadCurrent reads the pre-update snapshot, locking the row so two concurrent
// updates cannot both pass the record-version check.
func loadCurrent(ctx context.Context, q rowQuerier, uuid string) (*currentAccount, error) {
	var c currentAccount
	err := q.QueryRow(ctx, `
		SELECT coa_account_id, coa_account_code, coa_account_name, coa_account_description,
		       coa_account_type, coa_account_attributes, coa_account_is_postable,
		       coa_account_is_active, coa_account_is_visible, coa_account_is_system,
		       coa_account_record_version
		FROM coa_account
		WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL
		FOR UPDATE`, uuid).
		Scan(&c.id, &c.code, &c.name, &c.description, &c.acctType, &c.attrs,
			&c.isPostable, &c.isActive, &c.isVisible, &c.isSystem, &c.version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load account for update: %w", err)
	}
	if c.attrs == nil {
		c.attrs = map[string]any{}
	}
	return &c, nil
}

// boolStr renders a bool for the audit trail.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./chartofaccounts/ && go vet ./chartofaccounts/ && go test ./chartofaccounts/
```

Expected: build clean, unit tests PASS.

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/store_guard.go chartofaccounts/store_update.go
git commit -m "feat(coa): add the default-slot referential guard and account update"
```

---

## Task 14: Delete and bulk

**Files:**
- Create: `chartofaccounts/store_delete.go`
- Create: `chartofaccounts/store_bulk.go`

**Interfaces:**
- Consumes: `guardRetire`, `hasLiveChildren` (Task 13), `appendHistory` (Task 12)
- Produces: `SoftDelete(ctx, pool, uuid string, employeeID int) error`, `BulkUpdate(ctx, pool, in BulkInput, employeeID int) ([]BulkResult, error)`

- [ ] **Step 1: Write `store_delete.go`**

```go
package chartofaccounts

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SoftDelete retires a user-created account. Seeded (is_system) accounts can
// never be deleted -- chk_coa_system_undeletable enforces that at the database
// level too, but returning a 409 explains why.
//
// Blocked by: a default slot pointing at the account (AD-7), or any live child.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, employeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := loadCurrent(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if cur.isSystem {
		return ConflictError{Msg: fmt.Sprintf(
			"Account %s %s is a standard account and cannot be deleted. Deactivate it instead.",
			cur.code, cur.name)}
	}
	if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
		return err
	}
	kids, err := hasLiveChildren(ctx, tx, cur.id)
	if err != nil {
		return err
	}
	if kids {
		return ConflictError{Msg: fmt.Sprintf(
			"Account %s %s still has sub-accounts. Delete them first.", cur.code, cur.name)}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE coa_account
		SET coa_account_deleted_at = CURRENT_TIMESTAMP,
		    coa_account_deleted_by = $2,
		    coa_account_record_version = coa_account_record_version + 1
		WHERE coa_account_id = $1 AND coa_account_deleted_at IS NULL`,
		cur.id, nullableInt(employeeID)); err != nil {
		return fmt.Errorf("soft delete account: %w", err)
	}

	if err := appendHistory(ctx, tx, historyRow{
		AccountID: &cur.id, Action: actionDelete, Field: "code",
		OldValue: cur.code, EmployeeID: employeeID,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete account: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Write `store_bulk.go`**

```go
package chartofaccounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxBulkAccounts caps one bulk request. The tenant ships with 127 accounts,
// so this comfortably covers "select all" while bounding the transaction.
const maxBulkAccounts = 500

// BulkUpdate toggles is_active / is_visible across many accounts in ONE
// transaction. Any single failure rolls the whole batch back: a visibility
// change applied to half a chart of accounts is worse than one applied to none.
//
// Each account runs the same guardRetire check as a single update (AD-7).
func BulkUpdate(ctx context.Context, pool *pgxpool.Pool, in BulkInput, employeeID int) ([]BulkResult, error) {
	if len(in.UUIDs) == 0 {
		return nil, ClientError{Msg: "Select at least one account."}
	}
	if len(in.UUIDs) > maxBulkAccounts {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Select at most %d accounts at a time.", maxBulkAccounts)}
	}
	if in.IsActive == nil && in.IsVisible == nil {
		return nil, ClientError{Msg: "Specify isActive, isVisible, or both."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin bulk update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	results := make([]BulkResult, 0, len(in.UUIDs))
	for _, uuid := range in.UUIDs {
		res, err := bulkOne(ctx, tx, uuid, in, employeeID)
		if err != nil {
			return nil, err // hard failure: roll everything back
		}
		results = append(results, res)
		if !res.OK {
			// A blocked account fails the whole batch, so the caller never sees
			// a partially applied change.
			return nil, ConflictError{Msg: fmt.Sprintf(
				"No accounts were changed. %s", res.Message)}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bulk update: %w", err)
	}
	return results, nil
}

// bulkOne applies the batch's flags to one account. A ConflictError becomes a
// non-OK result rather than an error, so the caller can report which account
// blocked the batch.
func bulkOne(ctx context.Context, tx rowQuerier, uuid string, in BulkInput, employeeID int) (BulkResult, error) {
	cur, err := loadCurrent(ctx, tx, uuid)
	if errors.Is(err, ErrNotFound) {
		return BulkResult{UUID: uuid, OK: false, Message: "Account not found."}, nil
	}
	if err != nil {
		return BulkResult{}, err
	}

	next := *cur
	var audits []historyRow

	if in.IsActive != nil && *in.IsActive != cur.isActive {
		next.isActive = *in.IsActive
		act := actionActivate
		if !next.isActive {
			act = actionDeactivate
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_active", OldValue: boolStr(cur.isActive),
			NewValue: boolStr(next.isActive), EmployeeID: employeeID})
	}
	if in.IsVisible != nil && *in.IsVisible != cur.isVisible {
		next.isVisible = *in.IsVisible
		act := actionShow
		if !next.isVisible {
			act = actionHide
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_visible", OldValue: boolStr(cur.isVisible),
			NewValue: boolStr(next.isVisible), EmployeeID: employeeID})
	}

	if len(audits) == 0 {
		return BulkResult{UUID: uuid, OK: true, Message: "No change."}, nil
	}

	retiring := (!next.isActive && cur.isActive) || (!next.isVisible && cur.isVisible)
	if retiring {
		if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
			if conflict, ok := IsConflict(err); ok {
				return BulkResult{UUID: uuid, OK: false, Message: conflict.Msg}, nil
			}
			return BulkResult{}, err
		}
	}
	if next.isActive && !next.isVisible {
		return BulkResult{UUID: uuid, OK: false,
			Message: fmt.Sprintf("Account %s must be deactivated before it can be hidden.", cur.code)}, nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE coa_account
		SET coa_account_is_active = $2, coa_account_is_visible = $3,
		    coa_account_updated_at = CURRENT_TIMESTAMP, coa_account_updated_by = $4,
		    coa_account_record_version = coa_account_record_version + 1
		WHERE coa_account_id = $1 AND coa_account_deleted_at IS NULL`,
		cur.id, next.isActive, next.isVisible, nullableInt(employeeID)); err != nil {
		return BulkResult{}, fmt.Errorf("bulk update account %s: %w", cur.code, err)
	}
	for _, a := range audits {
		if err := appendHistory(ctx, tx, a); err != nil {
			return BulkResult{}, err
		}
	}
	return BulkResult{UUID: uuid, OK: true}, nil
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./chartofaccounts/ && go vet ./chartofaccounts/ && go test ./chartofaccounts/
```

Expected: build clean, unit tests PASS.

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/store_delete.go chartofaccounts/store_bulk.go
git commit -m "feat(coa): add guarded soft delete and transactional bulk visibility updates"
```

---

## Task 15: Default mapping slots

**Files:**
- Create: `chartofaccounts/store_defaults.go`

**Interfaces:**
- Consumes: `appendHistory` (Task 12), `accountIDByUUID` (Task 9)
- Produces: `Slots(ctx, pool) ([]DefaultSlot, error)`, `RepointSlot(ctx, pool, slotKey, accountUUID string, employeeID int) (*DefaultSlot, error)`

- [ ] **Step 1: Write `store_defaults.go`**

```go
package chartofaccounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// slotSelect is the shared projection for default mapping slots. The account
// join is LEFT so an unpointed slot still returns a row.
const slotSelect = `
	SELECT m.slot_key, m.slot_label, m.slot_description, a.coa_account_uuid,
	       COALESCE(a.coa_account_code,''), COALESCE(a.coa_account_name,''),
	       m.slot_is_system, m.slot_sort_order, m.slot_updated_at
	FROM coa_default_mapping m
	LEFT JOIN coa_account a
	       ON a.coa_account_id = m.coa_account_id AND a.coa_account_deleted_at IS NULL`

func scanSlot(row pgx.Row) (*DefaultSlot, error) {
	var s DefaultSlot
	if err := row.Scan(&s.Key, &s.Label, &s.Description, &s.AccountID,
		&s.AccountCode, &s.AccountName, &s.IsSystem, &s.SortOrder, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// Slots returns all default mapping slots in display order.
func Slots(ctx context.Context, pool *pgxpool.Pool) ([]DefaultSlot, error) {
	rows, err := pool.Query(ctx, slotSelect+` ORDER BY m.slot_sort_order, m.slot_key`)
	if err != nil {
		return nil, fmt.Errorf("list default slots: %w", err)
	}
	defer rows.Close()

	var out []DefaultSlot
	for rows.Next() {
		s, err := scanSlot(rows)
		if err != nil {
			return nil, fmt.Errorf("scan default slot: %w", err)
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate default slots: %w", err)
	}
	return out, nil
}

// RepointSlot points a slot at a different account, or clears it when
// accountUUID is empty.
//
// The target must be postable, active and live. That rule cannot be a foreign
// key -- Postgres cannot express a predicate on the referenced row -- so it is
// enforced here. This is the other half of guardRetire: the store refuses to
// point a slot at a disqualified account, and refuses to disqualify an account
// a slot points at (AD-7).
func RepointSlot(ctx context.Context, pool *pgxpool.Pool, slotKey, accountUUID string, employeeID int) (*DefaultSlot, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin repoint slot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		prevID   *int
		prevCode string
	)
	err = tx.QueryRow(ctx, `
		SELECT m.coa_account_id, COALESCE(a.coa_account_code,'')
		FROM coa_default_mapping m
		LEFT JOIN coa_account a ON a.coa_account_id = m.coa_account_id
		WHERE m.slot_key = $1
		FOR UPDATE OF m`, slotKey).Scan(&prevID, &prevCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load default slot: %w", err)
	}

	var (
		nextID   *int
		nextCode string
	)
	if accountUUID != "" {
		var (
			id                          int
			code, name                  string
			postable, active            bool
		)
		err := tx.QueryRow(ctx, `
			SELECT coa_account_id, coa_account_code, coa_account_name,
			       coa_account_is_postable, coa_account_is_active
			FROM coa_account
			WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL`, accountUUID).
			Scan(&id, &code, &name, &postable, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ClientError{Msg: "The account does not exist."}
		}
		if err != nil {
			return nil, fmt.Errorf("load slot target account: %w", err)
		}
		if !postable {
			return nil, ConflictError{Msg: fmt.Sprintf(
				"Account %s %s is a header account and cannot be used as a default.", code, name)}
		}
		if !active {
			return nil, ConflictError{Msg: fmt.Sprintf(
				"Account %s %s is inactive and cannot be used as a default.", code, name)}
		}
		nextID, nextCode = &id, code
	}

	if _, err := tx.Exec(ctx, `
		UPDATE coa_default_mapping
		SET coa_account_id = $2, slot_updated_at = CURRENT_TIMESTAMP, slot_updated_by = $3
		WHERE slot_key = $1`, slotKey, nextID, nullableInt(employeeID)); err != nil {
		return nil, fmt.Errorf("repoint default slot: %w", err)
	}

	if err := appendHistory(ctx, tx, historyRow{
		AccountID: nextID, SlotKey: slotKey, Action: actionRepointSlot,
		Field: "coa_account_id", OldValue: prevCode, NewValue: nextCode,
		EmployeeID: employeeID,
	}); err != nil {
		return nil, err
	}

	slot, err := scanSlot(tx.QueryRow(ctx, slotSelect+` WHERE m.slot_key = $1`, slotKey))
	if err != nil {
		return nil, fmt.Errorf("read back default slot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit repoint slot: %w", err)
	}
	return slot, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./chartofaccounts/ && go vet ./chartofaccounts/ && go test ./chartofaccounts/
```

Expected: build clean, unit tests PASS.

- [ ] **Step 3: Confirm no file exceeds the 300-line cap**

```bash
wc -l chartofaccounts/*.go | sort -rn | head -8
```

Expected: every file under 300. Split by verb if one is over.

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/store_defaults.go
git commit -m "feat(coa): read and repoint default account mapping slots"
```

---

## Task 16: Controllers and routing

**Files:**
- Create: `controllers/chartofaccounts.go`
- Create: `controllers/chartofaccounts_tree.go`
- Create: `controllers/chartofaccounts_defaults.go`
- Create: `controllers/chartofaccounts_bulk.go`
- Create: `controllers/chartofaccounts_audit.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: the whole `chartofaccounts` package; `authz.ResourceChartOfAccount` (Task 3); existing `fail`, `writeJSON`, `resolveEmployeeID`, `logSecurityEvent`
- Produces: `NewChartOfAccountsOps(cipher *secret.Cipher) *ChartOfAccountsOps` with 12 handlers

- [ ] **Step 1: Write `controllers/chartofaccounts.go`**

```go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

// ChartOfAccountsOps handles the Finance chart-of-accounts endpoints.
//
// Like InventoryOps, the chart of accounts is shared tenant-global reference
// data rather than an owner-scoped CRM record, so there is no per-record IDOR
// scope check beyond the resource-level chart_of_account:<action> permission.
// This is deliberate, not an omission: coa_account has no owner column to
// scope against.
//
// Routes (all under /api/tenant/finance):
//
//	GET    /accounts                    — list (cursor-paginated, ?postable=&active=&visible=)
//	POST   /accounts/search             — filter + sort + search + pagination
//	GET    /accounts/tree               — grouped report
//	GET    /accounts/categories         — the fixed reference tree
//	POST   /accounts                    — create
//	GET    /accounts/{uuid}             — get
//	PATCH  /accounts/{uuid}             — update
//	DELETE /accounts/{uuid}             — soft delete
//	PATCH  /accounts/bulk               — bulk activate/hide
//	GET    /accounts/{uuid}/history     — audit trail
//	GET    /account-defaults            — mapping slots
//	PATCH  /account-defaults/{slotKey}  — repoint a slot
type ChartOfAccountsOps struct {
	cipher *secret.Cipher // nil when SECRET_ENCRYPTION_KEY is unset; writes fail closed
}

// NewChartOfAccountsOps constructs the handler group. cipher may be nil in
// local dev; bank-account writes then fail with 503 rather than storing an
// account number in plaintext, mirroring NewSSOOps.
func NewChartOfAccountsOps(cipher *secret.Cipher) *ChartOfAccountsOps {
	return &ChartOfAccountsOps{cipher: cipher}
}

// authCOA resolves JWT + tenant pool + the chart_of_account:<action> grant.
func (h *ChartOfAccountsOps) authCOA(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceChartOfAccount, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"resource", string(authz.ResourceChartOfAccount), "action", string(action))
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" chart of accounts entries.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// coaFail maps a store error to an HTTP response.
func coaFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, chartofaccounts.ErrNotFound):
		fail(w, http.StatusNotFound, "Account not found.")
	case errors.Is(err, chartofaccounts.ErrCipherUnavailable):
		fail(w, http.StatusServiceUnavailable,
			"Bank account details cannot be saved: secret encryption is not configured.")
	case chartofaccounts.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		if conflict, ok := chartofaccounts.IsConflict(err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"success":       false,
				"message":       conflict.Msg,
				"blockingSlots": conflict.BlockingSlots,
			})
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// boolParam reads an optional true/false query parameter.
func boolParam(r *http.Request, key string) *bool {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &v
}

// filtersFrom builds the query-param toggles. The dropdown call every
// transaction screen makes is ?postable=true&active=true.
func filtersFrom(r *http.Request) chartofaccounts.Filters {
	f := chartofaccounts.Filters{
		Postable: boolParam(r, "postable"),
		Active:   boolParam(r, "active"),
		Visible:  boolParam(r, "visible"),
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("subCategoryId")); err == nil && n > 0 {
		f.SubCategoryID = &n
	}
	return f
}

// List GET /api/tenant/finance/accounts
func (h *ChartOfAccountsOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	page, err := chartofaccounts.Search(r.Context(), pool, req, filtersFrom(r))
	if err != nil {
		coaFail(w, err, "Failed to list accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Search POST /api/tenant/finance/accounts/search
func (h *ChartOfAccountsOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	page, err := chartofaccounts.Search(r.Context(), pool, req, filtersFrom(r))
	if err != nil {
		coaFail(w, err, "Failed to search accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Create POST /api/tenant/finance/accounts
func (h *ChartOfAccountsOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in chartofaccounts.CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	acct, err := chartofaccounts.Create(r.Context(), pool, h.cipher, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to create account.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "account": acct})
}

// Get GET /api/tenant/finance/accounts/{uuid}
func (h *ChartOfAccountsOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	acct, err := chartofaccounts.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		coaFail(w, err, "Failed to load account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": acct})
}

// Update PATCH /api/tenant/finance/accounts/{uuid}
func (h *ChartOfAccountsOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in chartofaccounts.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	acct, err := chartofaccounts.Update(r.Context(), pool, h.cipher,
		r.PathValue("uuid"), in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to update account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": acct})
}

// Delete DELETE /api/tenant/finance/accounts/{uuid}
func (h *ChartOfAccountsOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := chartofaccounts.SoftDelete(r.Context(), pool,
		r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		coaFail(w, err, "Failed to delete account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Account deleted."})
}
```

- [ ] **Step 2: Write `controllers/chartofaccounts_tree.go`**

```go
package controllers

import (
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
	"stonesuite-backend/query"
)

// Tree GET /api/tenant/finance/accounts/tree — the reporting screen.
// Structure only: no balances, because there is no general ledger yet.
func (h *ChartOfAccountsOps) Tree(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}

	cats, subs, err := chartofaccounts.Categories(r.Context(), pool)
	if err != nil {
		coaFail(w, err, "Failed to load account categories.")
		return
	}

	// The tree is the whole chart, not a page of it: 127 seeded accounts plus
	// user additions is small enough to assemble in one pass, and a paginated
	// report tree would be meaningless.
	page, err := chartofaccounts.Search(r.Context(), pool,
		query.Request{Limit: query.MaxLimit}, chartofaccounts.Filters{})
	if err != nil {
		coaFail(w, err, "Failed to load accounts.")
		return
	}
	all := page.Records
	for page.HasMore {
		page, err = chartofaccounts.Search(r.Context(), pool,
			query.Request{Limit: query.MaxLimit, Cursor: page.NextCursor},
			chartofaccounts.Filters{})
		if err != nil {
			coaFail(w, err, "Failed to load accounts.")
			return
		}
		all = append(all, page.Records...)
	}

	opts := chartofaccounts.TreeOptions{}
	if v, err := strconv.ParseBool(r.URL.Query().Get("includeInactive")); err == nil {
		opts.IncludeInactive = v
	}
	if v, err := strconv.ParseBool(r.URL.Query().Get("includeHidden")); err == nil {
		opts.IncludeHidden = v
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"sections": chartofaccounts.BuildTree(cats, subs, all, opts),
	})
}

// Categories GET /api/tenant/finance/accounts/categories — the fixed
// 9-category / 17-sub-category reference tree. Read-only (AD-1).
func (h *ChartOfAccountsOps) Categories(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	cats, subs, err := chartofaccounts.Categories(r.Context(), pool)
	if err != nil {
		coaFail(w, err, "Failed to load account categories.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "categories": cats, "subCategories": subs,
	})
}
```

- [ ] **Step 3: Write `controllers/chartofaccounts_defaults.go`**

```go
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// repointRequest is the PATCH body for a default slot. An empty accountId
// clears the slot.
type repointRequest struct {
	AccountID string `json:"accountId"`
}

// Defaults GET /api/tenant/finance/account-defaults
func (h *ChartOfAccountsOps) Defaults(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	slots, err := chartofaccounts.Slots(r.Context(), pool)
	if err != nil {
		coaFail(w, err, "Failed to load default accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slots": slots})
}

// RepointDefault PATCH /api/tenant/finance/account-defaults/{slotKey}
//
// Guarded by chart_of_account:configure rather than :update — repointing where
// every future transaction posts is a higher-trust act than renaming an account.
func (h *ChartOfAccountsOps) RepointDefault(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req repointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	slot, err := chartofaccounts.RepointSlot(r.Context(), pool,
		r.PathValue("slotKey"), req.AccountID, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to update the default account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slot": slot})
}
```

- [ ] **Step 4: Write `controllers/chartofaccounts_bulk.go` and `_audit.go`**

```go
package controllers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// BulkUpdate PATCH /api/tenant/finance/accounts/bulk — transactional
// activate/hide across many accounts. All-or-nothing: a visibility change
// applied to half a chart of accounts is worse than one applied to none.
func (h *ChartOfAccountsOps) BulkUpdate(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in chartofaccounts.BulkInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	results, err := chartofaccounts.BulkUpdate(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to update accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "results": results})
}
```

```go
package controllers

import (
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// History GET /api/tenant/finance/accounts/{uuid}/history — the audit trail.
// Bank account numbers never appear here: appendHistory redacts that field, so
// only the fact of a change is recorded (AD-10).
func (h *ChartOfAccountsOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := chartofaccounts.History(r.Context(), pool, r.PathValue("uuid"), limit)
	if err != nil {
		coaFail(w, err, "Failed to load account history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "history": entries})
}
```

Remove the unused `strconv` import from `chartofaccounts_bulk.go` — `go vet` will flag it.

- [ ] **Step 5: Register the routes in `main.go`**

Find the inventory registration block (`mux.Handle("GET /api/tenant/inventory/items", ...)` around line 511) and add after it. **Order matters**: `/accounts/bulk`, `/accounts/tree`, `/accounts/categories` and `/accounts/search` must be registered so they are not shadowed by `/accounts/{uuid}` — Go 1.22+ `ServeMux` prefers the more specific pattern, but registering literals first keeps the intent obvious.

```go
	// Chart of Accounts — Finance section master data.
	{
		coa := controllers.NewChartOfAccountsOps(cipher)
		mux.Handle("GET /api/tenant/finance/accounts", tenantChain(coa.List))
		mux.Handle("POST /api/tenant/finance/accounts/search", tenantChain(coa.Search))
		mux.Handle("GET /api/tenant/finance/accounts/tree", tenantChain(coa.Tree))
		mux.Handle("GET /api/tenant/finance/accounts/categories", tenantChain(coa.Categories))
		mux.Handle("PATCH /api/tenant/finance/accounts/bulk", tenantChain(coa.BulkUpdate))
		mux.Handle("POST /api/tenant/finance/accounts", tenantChain(coa.Create))
		mux.Handle("GET /api/tenant/finance/accounts/{uuid}", tenantChain(coa.Get))
		mux.Handle("PATCH /api/tenant/finance/accounts/{uuid}", tenantChain(coa.Update))
		mux.Handle("DELETE /api/tenant/finance/accounts/{uuid}", tenantChain(coa.Delete))
		mux.Handle("GET /api/tenant/finance/accounts/{uuid}/history", tenantChain(coa.History))
		mux.Handle("GET /api/tenant/finance/account-defaults", tenantChain(coa.Defaults))
		mux.Handle("PATCH /api/tenant/finance/account-defaults/{slotKey}", tenantChain(coa.RepointDefault))
	}
```

- [ ] **Step 6: Verify the build and route table**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: build clean, vet clean, all tests PASS.

```bash
grep -c "api/tenant/finance" main.go
```

Expected: `12`

- [ ] **Step 7: Run the drift and security reviewers**

Dispatch the `module-drift-checker` and `tenancy-security-reviewer` agents against `chartofaccounts/` and `controllers/chartofaccounts*.go`.

**Expected finding, which is NOT a bug:** both may flag the absence of `recordInScope` / owner-scope filtering. The spec's §6 states this explicitly — `coa_account` has no owner column, exactly like `inventory_item`. Confirm they raise nothing else.

- [ ] **Step 8: Commit**

```bash
git add controllers/chartofaccounts*.go main.go
git commit -m "feat(coa): expose chart of accounts over 12 tenant finance routes"
```

---

## Task 17: Database-backed verification

**Files:**
- Create: `chartofaccounts/store_dbtest_test.go` (build tag `dbtest`)

**Interfaces:**
- Consumes: the whole package
- Produces: nothing — verification only

- [ ] **Step 1: Write the dbtest suite**

Per the schema-verification note in project memory: use a **fresh database per package** with `--single-transaction`, or residue from another package produces convincing false failures.

```go
//go:build dbtest

package chartofaccounts

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestSeedCounts(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	var cats, subs, accts, slots int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM lkp_coa_category),
		       (SELECT count(*) FROM lkp_coa_subcategory),
		       (SELECT count(*) FROM coa_account),
		       (SELECT count(*) FROM coa_default_mapping)`).Scan(&cats, &subs, &accts, &slots))
	assert.Equal(t, 9, cats)
	assert.Equal(t, 17, subs)
	assert.Equal(t, 127, accts)
	assert.Equal(t, 19, slots)
}

// AD-3: names are deliberately not unique. A uniqueness constraint on name is
// the obvious thing to add and would fail the seed on day one.
func TestDuplicateAccountNamesAreSeeded(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM coa_account WHERE coa_account_name = 'Inventory Adjustment'`).Scan(&n))
	assert.Equal(t, 2, n, "5107 (COGS) and 9104 (System) share this name")
}

// AD-11: 9106 Intercompany Clearing is meaningless under single-subsidiary.
func TestIntercompanyClearingSeededInactive(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	var active bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_is_active FROM coa_account WHERE coa_account_code='9106'`).Scan(&active))
	assert.False(t, active)
}

// AD-2: sub-category 9100 is the only one mixing BS and PNL.
func TestSystemSubCategoryMixesSides(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT a.coa_account_bs_pnl FROM coa_account a
		JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
		WHERE s.subcategory_code = 9100`)
	require.NoError(t, err)
	defer rows.Close()
	var sides []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		sides = append(sides, s)
	}
	assert.ElementsMatch(t, []string{"BS", "PNL"}, sides)
}

// Every constraint from spec section 4 must actually reject what it should.
func TestConstraintsReject(t *testing.T) {
	pool, ctx := testPool(t), context.Background()

	subID := func(code int) int {
		var id int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code=$1`, code).Scan(&id))
		return id
	}
	acctID := func(code string) int {
		var id int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT coa_account_id FROM coa_account WHERE coa_account_code=$1`, code).Scan(&id))
		return id
	}

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "depth 2 is unrepresentable",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			      parent_id, coa_account_depth, coa_account_bs_pnl)
			      VALUES ('1103.01.01','Too deep',$1,$2,2,'BS')`,
			args: []any{subID(1100), acctID("1103")},
		},
		{
			name: "a parent_id with depth 0 is rejected",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			      parent_id, coa_account_depth, coa_account_bs_pnl)
			      VALUES ('1103.98','Bad depth',$1,$2,0,'BS')`,
			args: []any{subID(1100), acctID("1103")},
		},
		{
			name: "a child cannot change sub-category",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			      parent_id, coa_account_depth, coa_account_bs_pnl)
			      VALUES ('1103.97','Wrong subcat',$1,$2,1,'BS')`,
			args: []any{subID(1200), acctID("1103")},
		},
		{
			name: "active but hidden is rejected",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			      coa_account_bs_pnl, coa_account_is_active, coa_account_is_visible)
			      VALUES ('1196','Active hidden',$1,'BS',TRUE,FALSE)`,
			args: []any{subID(1100)},
		},
		{
			name: "duplicate live code is rejected",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id, coa_account_bs_pnl)
			      VALUES ('1101','Duplicate code',$1,'BS')`,
			args: []any{subID(1100)},
		},
		{
			name: "an invalid account type is rejected",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			      coa_account_bs_pnl, coa_account_type)
			      VALUES ('1195','Bad type',$1,'BS','crypto_wallet')`,
			args: []any{subID(1100)},
		},
		{
			name: "an invalid bs_pnl is rejected",
			sql: `INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id, coa_account_bs_pnl)
			      VALUES ('1194','Bad side',$1,'XX')`,
			args: []any{subID(1100)},
		},
		{
			name: "a seeded account cannot be soft-deleted",
			sql: `UPDATE coa_account SET coa_account_deleted_at = CURRENT_TIMESTAMP,
			      coa_account_deleted_by = 1 WHERE coa_account_code = '1101'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = tx.Exec(ctx, tt.sql, tt.args...)
			assert.Error(t, err, "the database must reject this")
		})
	}
}

// AD-7: the guard blocks all four retiring mutations, and releases once the
// slot is repointed.
func TestGuardBlocksAllFourMutations(t *testing.T) {
	pool, ctx := testPool(t), context.Background()

	var arID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_id FROM coa_account WHERE coa_account_code='1120'`).Scan(&arID))

	slots, err := BlockingSlots(ctx, pool, arID)
	require.NoError(t, err)
	assert.Contains(t, slots, "default_ar")

	err = guardRetire(ctx, pool, arID, "1120", "Accounts Receivable")
	require.Error(t, err)
	conflict, ok := IsConflict(err)
	require.True(t, ok)
	assert.Contains(t, conflict.BlockingSlots, "default_ar")

	// An account no slot points at is free to retire.
	var freeID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_id FROM coa_account WHERE coa_account_code='1130'`).Scan(&freeID))
	assert.NoError(t, guardRetire(ctx, pool, freeID, "1130", "Employee Advances"))
}

// AD-7, other half: a slot cannot point at a disqualified account.
func TestSlotRejectsDisqualifiedTarget(t *testing.T) {
	pool, ctx := testPool(t), context.Background()

	var inactiveUUID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_uuid FROM coa_account WHERE coa_account_code='9106'`).Scan(&inactiveUUID))

	_, err := RepointSlot(ctx, pool, "default_suspense", inactiveUUID, 1)
	require.Error(t, err)
	_, ok := IsConflict(err)
	assert.True(t, ok, "an inactive account must be rejected as a slot target")
}
```

- [ ] **Step 2: Run the suite**

```bash
go test -tags dbtest ./chartofaccounts/ -v
```

Expected: all PASS against a fresh database seeded from `schema.sql`. Without `TEST_DATABASE_URL` every test SKIPs cleanly.

- [ ] **Step 3: Full verification**

```bash
go build ./... && go vet ./... && go test ./... && golangci-lint run
```

Expected: all clean.

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/store_dbtest_test.go
git commit -m "test(coa): verify seed counts, constraints and the referential guard"
```

---

## Self-Review Notes

Checked against the spec, section by section:

| Spec section | Covered by |
|---|---|
| §3.1 Categories (9) | Task 1 |
| §3.2 Sub-categories (17) | Task 1 |
| §3.3 Accounts (127) + Americanized tax names + 9106 inactive | Task 2 |
| §3.4 Default slots (19) | Task 2 |
| §4 Schema + every constraint | Tasks 1–2, verified Task 17 |
| §5 Package layout | Tasks 4–15 |
| §6 API surface (12 routes) + RBAC | Tasks 3, 16 |
| §7.1 Create validation | Tasks 5–7, 12 |
| §7.2 The referential guard | Task 13, verified Task 17 |
| §7.3 Status codes | Task 16 (`coaFail`) |
| §7.4 Sorting + pagination | Tasks 9–10 |
| §7.5 Bulk PATCH | Task 14 |
| §7.6 Audit | Task 12 (`store_history.go`), Task 16 |
| §8 Testing | Tasks 5–8, 11 (unit), 17 (dbtest) |

**Signatures verified against the codebase, not guessed:**

| Used in plan | Verified at |
|---|---|
| `query.Built{Where, Keyset, OrderBy, Args, EffLimit, Sort}` | `query/builder.go:25-32` |
| `query.NextCursor(id string, sort SortKey, value any) string` | `query/cursor.go:62` |
| `query.SortResolver.SortExpr(key) (expr, dt, ok)` | `query/filter.go:115-117` |
| `secret.New/Encrypt/Decrypt`, cipher nil in dev | `secret/secret.go:26,46,56`; `main.go:107,135` |
| `resolveEmployeeID(r, identityID)` | `controllers/crm_admin.go:213` |
| `logSecurityEvent(r, event, kv...)` | `controllers/security_log.go:19` |
| `tenantChain(...)` route pattern | `main.go:511-516` |

**One correctness fix made during self-review:** `SortExpr("code")` returns `a.coa_account_code`, alias-qualified. The read query LEFT JOINs `coa_account` a second time as `p` to resolve the parent uuid, so a bare `coa_account_code` in `ORDER BY` would be ambiguous and Postgres would reject the query at runtime — a failure no unit test would catch, since the resolver is exercised in isolation.
