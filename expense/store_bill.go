// expense/store_bill.go — the vendor bill an approved claim raises.
package expense

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/employee"
	"stonesuite-backend/vendorbill"
)

// approvedStatusCode is the status a claim holds once approved -- the point at
// which its vendor bill is raised.
const approvedStatusCode = "APPV"

// createBillTx raises the Draft vendor bill for a claim that has just reached
// Approved, inside the caller's transaction: the claimant's Employee vendor
// (created on the spot for an employee that never had one) is who the bill is
// from, and each claim line becomes one bill line. The bill then runs the
// ordinary vendor bill workflow; paying it marks the claim Paid. A claim that
// already has a live bill never gets a second; a bill that was voided does not
// count, so the claim can be billed again (see CreateBill).
func createBillTx(ctx context.Context, tx pgx.Tx, expenseID, actorEmployeeID int) error {
	has, err := hasLiveBillTx(ctx, tx, expenseID)
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	var number, memo string
	var claimantID int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(expense_number,''), expense_memo, expense_claimant_id
		FROM expense WHERE expense_id = $1`, expenseID).Scan(&number, &memo, &claimantID); err != nil {
		return fmt.Errorf("load expense for bill: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT ec.expense_category_name, to_char(ei.expense_date,'YYYY-MM-DD'), ei.amount, ei.description
		FROM expense_item ei
		JOIN lkp_expense_category ec ON ec.expense_category_id = ei.category_id
		WHERE ei.expense_id = $1 AND ei.item_deleted_at IS NULL
		ORDER BY ei.line_number`, expenseID)
	if err != nil {
		return fmt.Errorf("load expense lines for bill: %w", err)
	}
	var lines []vendorbill.ExpenseLine
	for rows.Next() {
		var category, date, description string
		var amount float64
		if err := rows.Scan(&category, &date, &amount, &description); err != nil {
			rows.Close()
			return fmt.Errorf("scan expense line for bill: %w", err)
		}
		lines = append(lines, vendorbill.ExpenseLine{Description: billLineDescription(category, date, description), Amount: amount})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load expense lines for bill: %w", err)
	}

	vendorUUID, err := employee.EnsureVendorTx(ctx, tx, claimantID, actorEmployeeID)
	if err != nil {
		return fmt.Errorf("vendor for expense claimant: %w", err)
	}
	if _, err := vendorbill.CreateFromExpenseTx(ctx, tx, vendorbill.ExpenseBillInput{
		ExpenseID: expenseID, VendorUUID: vendorUUID, ExpenseNumber: number, Memo: memo,
		OwnerEmployeeID: actorEmployeeID, Lines: lines,
	}, actorEmployeeID); err != nil {
		return fmt.Errorf("create vendor bill for expense: %w", err)
	}
	return nil
}

// hasLiveBillTx reports whether the claim has a vendor bill that is neither
// deleted nor voided.
func hasLiveBillTx(ctx context.Context, tx pgx.Tx, expenseID int) (bool, error) {
	var has bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM vendor_bill b
			JOIN lkp_record_status s ON s.record_status_id = b.vendor_bill_status
			WHERE b.vendor_bill_expense_id = $1 AND b.vendor_bill_deleted_at IS NULL
			  AND s.record_status_code <> 'VOID')`, expenseID).Scan(&has); err != nil {
		return false, fmt.Errorf("check expense bill: %w", err)
	}
	return has, nil
}

// billLineDescription is the text of a bill line made from a claim line:
// "Category - what it was (date)".
func billLineDescription(category, date, description string) string {
	text := category
	if d := strings.TrimSpace(description); d != "" {
		text += " - " + d
	}
	return text + " (" + date + ")"
}
