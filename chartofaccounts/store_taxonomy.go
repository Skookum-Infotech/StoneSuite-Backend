package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxTaxonomyNameLength matches category_name/subcategory_name VARCHAR(60).
// Checked here so an over-long name is a 400 explaining the limit rather than a
// 22001 from Postgres the controller can only render as a 500.
const MaxTaxonomyNameLength = 60

// Taxonomy kinds, matching chk_coa_taxonomy_kind in tenant/schema.sql.
const (
	taxonomyCategory    = "category"
	taxonomySubCategory = "subcategory"
)

// Normal-balance markers, matching chk_coa_category_balance.
const (
	NormalBalanceDebit  = "debit"
	NormalBalanceCredit = "credit"
)

// categorySelect / subCategorySelect are the read-back projections, ordered to
// match Categories() so both paths scan identically.
const categorySelect = `
	SELECT category_id, category_code, category_name, category_range_low,
	       category_range_high, category_normal_balance, category_bs_pnl, category_sort_order
	FROM lkp_coa_category`

const subCategorySelect = `
	SELECT s.subcategory_id, s.category_id, c.category_code, s.subcategory_code,
	       s.subcategory_name, s.subcategory_range_low, s.subcategory_range_high,
	       s.subcategory_sort_order
	FROM lkp_coa_subcategory s
	JOIN lkp_coa_category c ON c.category_id = s.category_id`

// CreateCategory appends a category. Its code, range and sort order are
// server-assigned -- NextCategoryCode hands out the lowest free thousand-block,
// so the first tenant-created category is 10000 (the nine seeded ones fill
// 1000-9000) and its accounts stay inside a range nothing else can allocate
// from.
func CreateCategory(ctx context.Context, pool *pgxpool.Pool, in CategoryCreateInput, employeeID int) (*Category, error) {
	name, err := validTaxonomyName(in.Name, "category")
	if err != nil {
		return nil, err
	}
	if !ValidCategorySide(in.BSPNL) {
		return nil, ClientError{Msg: fmt.Sprintf(
			"bsPnl must be %q, %q or %q, got %q.",
			BalanceSheet, ProfitAndLoss, MixedSide, in.BSPNL)}
	}
	if in.NormalBalance != NormalBalanceDebit && in.NormalBalance != NormalBalanceCredit {
		return nil, ClientError{Msg: fmt.Sprintf(
			"normalBalance must be %q or %q, got %q.",
			NormalBalanceDebit, NormalBalanceCredit, in.NormalBalance)}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create category: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	taken, err := takenCategoryCodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	code, err := NextCategoryCode(taken)
	if err != nil {
		return nil, err
	}

	var newID int
	err = tx.QueryRow(ctx, `
		INSERT INTO lkp_coa_category
			(category_code, category_name, category_range_low, category_range_high,
			 category_normal_balance, category_bs_pnl, category_sort_order)
		VALUES ($1,$2,$3,$4,$5,$6,
		        (SELECT COALESCE(MAX(category_sort_order),0)+1 FROM lkp_coa_category))
		RETURNING category_id`,
		code, name, code, code+CategoryBlockSize-1,
		in.NormalBalance, in.BSPNL).Scan(&newID)
	if isUniqueViolation(err) {
		return nil, ConflictError{Msg: fmt.Sprintf(
			"Category code %d was just taken. Please retry.", code)}
	}
	if err != nil {
		return nil, fmt.Errorf("insert category: %w", err)
	}

	if err := appendTaxonomyHistory(ctx, tx, taxonomyHistoryRow{
		Kind: taxonomyCategory, Code: code, Action: actionCreate,
		Field: "name", NewValue: name, EmployeeID: employeeID,
	}); err != nil {
		return nil, err
	}

	cat, err := scanCategory(tx.QueryRow(ctx, categorySelect+` WHERE category_id = $1`, newID))
	if err != nil {
		return nil, fmt.Errorf("read back created category: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create category: %w", err)
	}
	return cat, nil
}

// RenameCategory changes a category's name. Name is the only mutable field:
// code and range are what every account code inside the category was allocated
// against, and normal balance and side are what its accounts' bs_pnl was
// derived from -- changing any of them would retroactively misfile accounts
// that are already posted to.
func RenameCategory(ctx context.Context, pool *pgxpool.Pool, id int, in RenameInput, employeeID int) (*Category, error) {
	name, err := validTaxonomyName(in.Name, "category")
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin rename category: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		code    int
		oldName string
	)
	err = tx.QueryRow(ctx,
		`SELECT category_code, category_name FROM lkp_coa_category WHERE category_id = $1`, id).
		Scan(&code, &oldName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load category: %w", err)
	}

	if oldName != name {
		if _, err := tx.Exec(ctx,
			`UPDATE lkp_coa_category SET category_name = $2 WHERE category_id = $1`, id, name); err != nil {
			return nil, fmt.Errorf("rename category: %w", err)
		}
		if err := appendTaxonomyHistory(ctx, tx, taxonomyHistoryRow{
			Kind: taxonomyCategory, Code: code, Action: actionUpdate,
			Field: "name", OldValue: oldName, NewValue: name, EmployeeID: employeeID,
		}); err != nil {
			return nil, err
		}
	}

	cat, err := scanCategory(tx.QueryRow(ctx, categorySelect+` WHERE category_id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("read back renamed category: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit rename category: %w", err)
	}
	return cat, nil
}

// CreateSubCategory appends a sub-category under an existing category. Its code
// and range are the lowest free hundred-block inside the parent's range, and it
// inherits the parent's side -- there is no per-sub-category side, which is
// exactly why DeriveBSPNL now keys off the category.
func CreateSubCategory(ctx context.Context, pool *pgxpool.Pool, in SubCategoryCreateInput, employeeID int) (*SubCategory, error) {
	name, err := validTaxonomyName(in.Name, "sub-category")
	if err != nil {
		return nil, err
	}
	if in.CategoryID <= 0 {
		return nil, ClientError{Msg: "A category is required for a sub-category."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create sub-category: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var catLow, catHigh int
	err = tx.QueryRow(ctx,
		`SELECT category_range_low, category_range_high FROM lkp_coa_category WHERE category_id = $1`,
		in.CategoryID).Scan(&catLow, &catHigh)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: fmt.Sprintf("Unknown category id %d.", in.CategoryID)}
	}
	if err != nil {
		return nil, fmt.Errorf("load category: %w", err)
	}

	taken, err := takenSubCategoryCodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	code, err := NextSubCategoryCode(catLow, catHigh, taken)
	if err != nil {
		return nil, err
	}

	var newID int
	err = tx.QueryRow(ctx, `
		INSERT INTO lkp_coa_subcategory
			(category_id, subcategory_code, subcategory_name, subcategory_range_low,
			 subcategory_range_high, subcategory_sort_order)
		VALUES ($1,$2,$3,$4,$5,
		        (SELECT COALESCE(MAX(subcategory_sort_order),0)+1
		           FROM lkp_coa_subcategory WHERE category_id = $1))
		RETURNING subcategory_id`,
		in.CategoryID, code, name, code, code+SubCategoryBlockSize-1).Scan(&newID)
	if isUniqueViolation(err) {
		return nil, ConflictError{Msg: fmt.Sprintf(
			"Sub-category code %d was just taken. Please retry.", code)}
	}
	if err != nil {
		return nil, fmt.Errorf("insert sub-category: %w", err)
	}

	if err := appendTaxonomyHistory(ctx, tx, taxonomyHistoryRow{
		Kind: taxonomySubCategory, Code: code, Action: actionCreate,
		Field: "name", NewValue: name, EmployeeID: employeeID,
	}); err != nil {
		return nil, err
	}

	sub, err := scanSubCategory(tx.QueryRow(ctx, subCategorySelect+` WHERE s.subcategory_id = $1`, newID))
	if err != nil {
		return nil, fmt.Errorf("read back created sub-category: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create sub-category: %w", err)
	}
	return sub, nil
}

// RenameSubCategory changes a sub-category's name, under the same rename-only
// rule as RenameCategory.
func RenameSubCategory(ctx context.Context, pool *pgxpool.Pool, id int, in RenameInput, employeeID int) (*SubCategory, error) {
	name, err := validTaxonomyName(in.Name, "sub-category")
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin rename sub-category: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		code    int
		oldName string
	)
	err = tx.QueryRow(ctx,
		`SELECT subcategory_code, subcategory_name FROM lkp_coa_subcategory WHERE subcategory_id = $1`, id).
		Scan(&code, &oldName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sub-category: %w", err)
	}

	if oldName != name {
		if _, err := tx.Exec(ctx,
			`UPDATE lkp_coa_subcategory SET subcategory_name = $2 WHERE subcategory_id = $1`,
			id, name); err != nil {
			return nil, fmt.Errorf("rename sub-category: %w", err)
		}
		if err := appendTaxonomyHistory(ctx, tx, taxonomyHistoryRow{
			Kind: taxonomySubCategory, Code: code, Action: actionUpdate,
			Field: "name", OldValue: oldName, NewValue: name, EmployeeID: employeeID,
		}); err != nil {
			return nil, err
		}
	}

	sub, err := scanSubCategory(tx.QueryRow(ctx, subCategorySelect+` WHERE s.subcategory_id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("read back renamed sub-category: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit rename sub-category: %w", err)
	}
	return sub, nil
}

// validTaxonomyName trims and bounds a category or sub-category name. noun is
// folded into the message so the caller reads "A category name is required."
// rather than a generic one.
func validTaxonomyName(raw, noun string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", ClientError{Msg: fmt.Sprintf("A %s name is required.", noun)}
	}
	if len([]rune(name)) > MaxTaxonomyNameLength {
		return "", ClientError{Msg: fmt.Sprintf(
			"A %s name is limited to %d characters.", noun, MaxTaxonomyNameLength)}
	}
	return name, nil
}

// taxonomyHistoryRow is one audited change to a category or sub-category.
type taxonomyHistoryRow struct {
	Kind       string
	Code       int
	Action     string
	Field      string
	OldValue   string
	NewValue   string
	EmployeeID int
}

// appendTaxonomyHistory writes one audit row. Like appendHistory it takes a
// rowQuerier so the audit lands in the caller's transaction -- a rolled-back
// rename must not leave a row claiming it happened. No redaction pass is needed
// here: the only field ever recorded is a display name.
func appendTaxonomyHistory(ctx context.Context, q rowQuerier, h taxonomyHistoryRow) error {
	_, err := q.Exec(ctx, `
		INSERT INTO coa_taxonomy_history
			(taxonomy_kind, taxonomy_code, history_action, history_field,
			 history_old_value, history_new_value, history_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		h.Kind, h.Code, h.Action, h.Field, h.OldValue, h.NewValue,
		nullableInt(h.EmployeeID))
	if err != nil {
		return fmt.Errorf("append taxonomy history: %w", err)
	}
	return nil
}

// takenCategoryCodes returns every category code in use, for NextCategoryCode.
func takenCategoryCodes(ctx context.Context, q rowQuerier) ([]int, error) {
	return scanIntColumn(ctx, q, `SELECT category_code FROM lkp_coa_category`, "category codes")
}

// takenSubCategoryCodes returns every sub-category code in use anywhere -- not
// just under the target category -- because subcategory_code is unique across
// the whole table (uq_coa_subcategory_code), not per category.
func takenSubCategoryCodes(ctx context.Context, q rowQuerier) ([]int, error) {
	return scanIntColumn(ctx, q, `SELECT subcategory_code FROM lkp_coa_subcategory`, "sub-category codes")
}

// scanIntColumn reads a single-int-column query into a slice. noun names the
// set for error wrapping.
func scanIntColumn(ctx context.Context, q rowQuerier, sql, noun string) ([]int, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", noun, err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan %s: %w", noun, err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", noun, err)
	}
	return out, nil
}

// scanCategory reads one row of categorySelect.
func scanCategory(row pgx.Row) (*Category, error) {
	var c Category
	if err := row.Scan(&c.ID, &c.Code, &c.Name, &c.RangeLow, &c.RangeHigh,
		&c.NormalBalance, &c.BSPNL, &c.SortOrder); err != nil {
		return nil, err
	}
	return &c, nil
}

// scanSubCategory reads one row of subCategorySelect.
func scanSubCategory(row pgx.Row) (*SubCategory, error) {
	var s SubCategory
	if err := row.Scan(&s.ID, &s.CategoryID, &s.CategoryCode, &s.Code, &s.Name,
		&s.RangeLow, &s.RangeHigh, &s.SortOrder); err != nil {
		return nil, err
	}
	return &s, nil
}
