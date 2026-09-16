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

// createTarget is the resolved placement of a new account. Exactly one of
// subCategoryID / categoryID is non-nil, mirroring chk_coa_placement; rangeLow
// and rangeHigh are the code window that placement allocates from.
type createTarget struct {
	subCategoryID *int
	categoryID    *int
	categorySide  string // BS | PNL | MIXED, from the resolved category
	rangeLow      int
	rangeHigh     int
	parentID      *int
	parentCode    string
	depth         int
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

	bsPnl, err := DeriveBSPNL(target.categorySide, in.BSPNL)
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
			 category_id, parent_id, coa_account_depth, coa_account_bs_pnl, coa_account_type,
			 coa_account_attributes, coa_account_is_postable, coa_account_created_by,
			 coa_account_updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		RETURNING coa_account_id`,
		code, strings.TrimSpace(in.Name), strings.TrimSpace(in.Description),
		target.subCategoryID, target.categoryID, target.parentID, target.depth, bsPnl, in.Type,
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

// resolveTarget works out where the new account goes: under a sub-category,
// directly under a category, or under a parent account. A child inherits its
// parent's placement verbatim (AD-5) and sits at depth 1; the two-level cap
// means a parent must itself be top-level (AD-4).
func resolveTarget(ctx context.Context, q rowQuerier, in CreateInput) (createTarget, error) {
	if in.ParentID != "" {
		return resolveParentTarget(ctx, q, in)
	}
	switch {
	case in.SubCategoryID > 0 && in.CategoryID > 0:
		return createTarget{}, ClientError{Msg: "An account belongs to either a sub-category or a category, not both."}
	case in.SubCategoryID > 0:
		return resolveSubCategoryTarget(ctx, q, in.SubCategoryID)
	case in.CategoryID > 0:
		return resolveCategoryTarget(ctx, q, in.CategoryID)
	default:
		return createTarget{}, ClientError{Msg: "A category or sub-category is required for a top-level account."}
	}
}

// resolveSubCategoryTarget places the account under a sub-category, allocating
// its code from that sub-category's hundred-block.
func resolveSubCategoryTarget(ctx context.Context, q rowQuerier, subCategoryID int) (createTarget, error) {
	var (
		t     createTarget
		subID int
	)
	err := q.QueryRow(ctx, `
		SELECT s.subcategory_id, s.subcategory_range_low, s.subcategory_range_high, c.category_bs_pnl
		FROM lkp_coa_subcategory s
		JOIN lkp_coa_category c ON c.category_id = s.category_id
		WHERE s.subcategory_id = $1`, subCategoryID).
		Scan(&subID, &t.rangeLow, &t.rangeHigh, &t.categorySide)
	if errors.Is(err, pgx.ErrNoRows) {
		return createTarget{}, ClientError{Msg: fmt.Sprintf(
			"Unknown sub-category id %d.", subCategoryID)}
	}
	if err != nil {
		return createTarget{}, fmt.Errorf("load sub-category: %w", err)
	}
	t.subCategoryID = &subID
	return t, nil
}

// resolveCategoryTarget places the account directly under a category,
// allocating its code from the category's reserved first block
// (CategoryDirectRange) so it can never collide with a sub-category's range.
func resolveCategoryTarget(ctx context.Context, q rowQuerier, categoryID int) (createTarget, error) {
	var (
		t               createTarget
		catID           int
		catLow, catHigh int
	)
	err := q.QueryRow(ctx, `
		SELECT category_id, category_range_low, category_range_high, category_bs_pnl
		FROM lkp_coa_category WHERE category_id = $1`, categoryID).
		Scan(&catID, &catLow, &catHigh, &t.categorySide)
	if errors.Is(err, pgx.ErrNoRows) {
		return createTarget{}, ClientError{Msg: fmt.Sprintf(
			"Unknown category id %d.", categoryID)}
	}
	if err != nil {
		return createTarget{}, fmt.Errorf("load category: %w", err)
	}
	t.categoryID = &catID
	t.rangeLow, t.rangeHigh = CategoryDirectRange(catLow, catHigh)
	return t, nil
}

// resolveParentTarget places the account under an existing top-level account,
// copying the parent's placement columns so the child lands on the same side of
// chk_coa_placement and satisfies whichever of fk_coa_parent_subcat /
// fk_coa_parent_category applies.
func resolveParentTarget(ctx context.Context, q rowQuerier, in CreateInput) (createTarget, error) {
	if !validAccountUUID(in.ParentID) {
		return createTarget{}, ClientError{Msg: fmt.Sprintf(
			"%q is not a valid account id.", in.ParentID)}
	}

	var (
		t           createTarget
		parentID    int
		parentDepth int
		subCode     *int
		catCode     int
	)
	err := q.QueryRow(ctx, `
		SELECT a.coa_account_id, a.coa_account_code, a.coa_account_depth,
		       a.subcategory_id, a.category_id, s.subcategory_code, c.category_code, c.category_bs_pnl
		FROM coa_account a
		LEFT JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
		JOIN lkp_coa_category c ON c.category_id = COALESCE(s.category_id, a.category_id)
		WHERE a.coa_account_uuid = $1 AND a.coa_account_deleted_at IS NULL`, in.ParentID).
		Scan(&parentID, &t.parentCode, &parentDepth,
			&t.subCategoryID, &t.categoryID, &subCode, &catCode, &t.categorySide)
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
	// A caller may echo the placement it thinks the parent has; disagreeing with
	// the parent is a client bug, not a request to move the account.
	if in.SubCategoryID > 0 && (t.subCategoryID == nil || in.SubCategoryID != *t.subCategoryID) {
		return createTarget{}, ClientError{Msg: placementMismatchMsg(subCode, catCode)}
	}
	if in.CategoryID > 0 && (t.categoryID == nil || in.CategoryID != *t.categoryID) {
		return createTarget{}, ClientError{Msg: placementMismatchMsg(subCode, catCode)}
	}
	t.parentID = &parentID
	t.depth = 1
	return t, nil
}

// placementMismatchMsg names the placement a sub-account is actually confined
// to, using whichever of the two the parent uses.
func placementMismatchMsg(subCode *int, catCode int) string {
	if subCode != nil {
		return fmt.Sprintf("A sub-account must stay in its parent's sub-category (%d).", *subCode)
	}
	return fmt.Sprintf("A sub-account must stay in its parent's category (%d).", catCode)
}
