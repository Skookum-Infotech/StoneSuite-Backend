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

	if !validAccountUUID(in.ParentID) {
		return createTarget{}, ClientError{Msg: fmt.Sprintf(
			"%q is not a valid account id.", in.ParentID)}
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
