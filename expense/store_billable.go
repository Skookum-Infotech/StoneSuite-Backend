// expense/store_billable.go — raising a vendor bill for an approved claim on
// demand, and listing the approved claims still waiting for one.
package expense

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// billableLimit caps the claims one picker request returns.
const billableLimit = 50

// BillableClaim is one approved claim that has no live vendor bill yet: enough
// for the Vendor Bill form's expense picker to name it.
type BillableClaim struct {
	ID           string  `json:"id"`
	Number       string  `json:"expenseNumber"`
	ClaimantName string  `json:"claimantName"`
	Total        float64 `json:"total"`
	Memo         string  `json:"memo,omitempty"`
}

// CreateBill raises the Draft vendor bill for an approved claim on demand -- for a
// claim approved before bills were raised automatically, or whose bill was
// voided. The claim must be Approved and have no live bill; the bill is the
// same one approval would have made (the claimant's Employee vendor, one line per
// claim line). The claim itself is unchanged: it stays Approved until the bill
// is paid.
func CreateBill(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Expense, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create expense bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT exp.expense_id, rs.record_status_code
		FROM expense exp JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL
		FOR UPDATE OF exp`, uuid).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load expense for bill: %w", err)
	}
	if statusCode != approvedStatusCode {
		return nil, ClientError{Msg: "Only an approved expense claim can be billed."}
	}
	has, err := hasLiveBillTx(ctx, tx, internalID)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, ClientError{Msg: "This expense claim already has a vendor bill."}
	}
	if err := createBillTx(ctx, tx, internalID, actorEmployeeID); err != nil {
		return nil, err
	}
	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create expense bill: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// Billable lists the approved claims, without a live vendor bill, whose claimant's
// Employee vendor is vendorUUID, newest first. search narrows by claim number or
// claimant name. Under a narrower than "all" scope only the caller's own claims
// (as claimant or filer) are listed, the same as the claims list.
func Billable(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID, vendorUUID, search string) ([]BillableClaim, error) {
	args := []any{vendorUUID, approvedStatusCode}
	where := []string{
		"exp.expense_deleted_at IS NULL",
		"rs.record_status_code = $2",
		`NOT EXISTS (
			SELECT 1 FROM vendor_bill b
			JOIN lkp_record_status s ON s.record_status_id = b.vendor_bill_status
			WHERE b.vendor_bill_expense_id = exp.expense_id AND b.vendor_bill_deleted_at IS NULL
			  AND s.record_status_code <> 'VOID')`,
	}
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return []BillableClaim{}, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("(exp.expense_claimant_id = $%d OR exp.expense_created_by = $%d)", len(args), len(args)))
	}
	if term := strings.TrimSpace(search); term != "" {
		args = append(args, term)
		n := strconv.Itoa(len(args))
		where = append(where, "(exp.expense_number ILIKE '%'||$"+n+"||'%' OR "+
			"(ce.employee_first_name || ' ' || ce.employee_last_name) ILIKE '%'||$"+n+"||'%')")
	}

	rows, err := pool.Query(ctx, `
		SELECT exp.expense_uuid, COALESCE(exp.expense_number,''),
		       COALESCE(NULLIF(TRIM(ce.employee_first_name || ' ' || ce.employee_last_name), ''), ce.employee_email),
		       exp.expense_total, exp.expense_memo
		FROM expense exp
		JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
		JOIN employee ce ON ce.employee_id = exp.expense_claimant_id
		JOIN vendor v ON v.vendor_employee_id = exp.expense_claimant_id
		             AND v.vendor_deleted_at IS NULL AND v.vendor_uuid = $1
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY exp.expense_updated_at DESC, exp.expense_id DESC
		LIMIT `+strconv.Itoa(billableLimit), args...)
	if err != nil {
		return nil, fmt.Errorf("list billable expense claims: %w", err)
	}
	defer rows.Close()
	out := []BillableClaim{}
	for rows.Next() {
		var c BillableClaim
		if err := rows.Scan(&c.ID, &c.Number, &c.ClaimantName, &c.Total, &c.Memo); err != nil {
			return nil, fmt.Errorf("scan billable expense claim: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
