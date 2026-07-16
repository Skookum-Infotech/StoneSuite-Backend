//go:build dbtest

package creditmemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedCustomer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	var custUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return custUUID
}

func seedItem(t *testing.T, pool *pgxpool.Pool, price float64) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var itemUUID string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, 'Test Item', 1, $2, 1) RETURNING inventory_item_uuid`, "CM-SKU-"+suffix, price).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return itemUUID
}

// seedSentInvoice creates an invoice for custUUID already transitioned to SENT
// (the only status family Apply accepts credit against).
func seedSentInvoice(t *testing.T, pool *pgxpool.Pool, custUUID string, amount float64) string {
	t.Helper()
	ctx := context.Background()
	itemUUID := seedItem(t, pool, amount)
	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: amount}},
	}, 1)
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	for _, st := range []string{"PAPV", "APPV", "SENT"} {
		if inv, err = invoice.Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition invoice to %s: %v", st, err)
		}
	}
	return inv.ID
}

// seedApprovedMemo creates a credit memo for custUUID at the given total and
// approves it.
func seedApprovedMemo(t *testing.T, pool *pgxpool.Pool, custUUID string, amount float64) *CreditMemo {
	t.Helper()
	ctx := context.Background()
	cm, err := Create(ctx, pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Reason:       "Returned goods",
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "Returned slab", Quantity: 1, UnitPrice: amount}},
	}, 1)
	if err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}
	cm, err = Transition(ctx, pool, cm.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("approve credit memo: %v", err)
	}
	return cm
}

func invoiceBalances(t *testing.T, pool *pgxpool.Pool, invUUID string) (grandTotal, amountPaid, creditTotal, balanceDue float64, statusCode string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT i.invoice_grand_total, i.invoice_amount_paid, i.invoice_credit_total, i.invoice_balance_due,
		       rs.record_status_code
		FROM invoice i JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1`, invUUID,
	).Scan(&grandTotal, &amountPaid, &creditTotal, &balanceDue, &statusCode); err != nil {
		t.Fatalf("load invoice balances: %v", err)
	}
	return
}

func TestCreate_StartsAsDraftWithNumberAndLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)

	cm, err := Create(ctx, pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Reason:       "Overbilled freight",
		Lines: []CreditMemoLineInput{
			{LineNumber: 1, Description: "Freight credit", Quantity: 2, UnitPrice: 50},
		},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(cm.Number, "CRDT-") {
		t.Errorf("Number = %q, want CRDT- prefix", cm.Number)
	}
	if cm.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", cm.StatusCode)
	}
	if cm.GrandTotal != 100 {
		t.Errorf("GrandTotal = %v, want 100", cm.GrandTotal)
	}
	if cm.UnappliedAmount != 100 {
		t.Errorf("UnappliedAmount = %v, want 100", cm.UnappliedAmount)
	}
	if len(cm.Lines) != 1 {
		t.Fatalf("len(Lines) = %d, want 1", len(cm.Lines))
	}
	// Free-text lines must carry a non-empty item name.
	if cm.Lines[0].ItemName == "" {
		t.Error("free-text line has empty ItemName")
	}
	if cm.CustomFields == nil {
		t.Error("CustomFields is nil, want empty map")
	}
}

func TestCreate_RequiresCustomerAndLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)

	var ce ClientError
	if _, err := Create(ctx, pool, CreateCreditMemoInput{
		Lines: []CreditMemoLineInput{{LineNumber: 1, Description: "x", Quantity: 1, UnitPrice: 1}},
	}, 1); !errors.As(err, &ce) {
		t.Errorf("create without customer: err = %v, want ClientError", err)
	}
	if _, err := Create(ctx, pool, CreateCreditMemoInput{CustomerUUID: custUUID}, 1); !errors.As(err, &ce) {
		t.Errorf("create without lines: err = %v, want ClientError", err)
	}
}

// A DRFT memo must not be able to move credit: the memo IS the authorization,
// so unapproved credit must never offset AR (spec AD-7).
func TestApply_RejectsDraftMemo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)

	cm, err := Create(ctx, pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "credit", Quantity: 1, UnitPrice: 50}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = Apply(ctx, pool, cm.ID, invUUID, 50, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("apply draft memo: err = %v, want ClientError", err)
	}
	if !strings.Contains(err.Error(), "approved") {
		t.Errorf("apply draft memo message = %q, want it to mention approval", err.Error())
	}
}

// The headline behavior: credit reduces balance_due WITHOUT touching
// amount_paid, which must keep meaning cash (spec AD-4).
func TestApply_PartialReducesBalanceButNotAmountPaid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 40)

	cm, err := Apply(ctx, pool, cm.ID, invUUID, 30, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cm.AppliedTotal != 30 || cm.UnappliedAmount != 10 {
		t.Errorf("memo rollup = applied %v / unapplied %v, want 30 / 10", cm.AppliedTotal, cm.UnappliedAmount)
	}
	// Partially applied, so the memo stays APPV, not APPL.
	if cm.StatusCode != "APPV" {
		t.Errorf("StatusCode = %q, want APPV (partially applied)", cm.StatusCode)
	}

	gt, paid, credit, balance, status := invoiceBalances(t, pool, invUUID)
	if gt != 100 {
		t.Errorf("grandTotal = %v, want 100", gt)
	}
	if paid != 0 {
		t.Errorf("amountPaid = %v, want 0 — credit must never be recorded as cash", paid)
	}
	if credit != 30 {
		t.Errorf("creditTotal = %v, want 30", credit)
	}
	if balance != 70 {
		t.Errorf("balanceDue = %v, want 70 (100 - 0 cash - 30 credit)", balance)
	}
	if status != "PART" {
		t.Errorf("invoice status = %q, want PART", status)
	}
}

// Fully applying a memo derives it to APPL (spec AD-13) and zeroes the invoice
// to PAID even though no cash was received.
func TestApply_FullDerivesAppliedAndPaid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	cm, err := Apply(ctx, pool, cm.ID, invUUID, 100, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cm.StatusCode != "APPL" {
		t.Errorf("StatusCode = %q, want APPL (fully applied)", cm.StatusCode)
	}
	if cm.UnappliedAmount != 0 {
		t.Errorf("UnappliedAmount = %v, want 0", cm.UnappliedAmount)
	}

	_, paid, credit, balance, status := invoiceBalances(t, pool, invUUID)
	if paid != 0 {
		t.Errorf("amountPaid = %v, want 0", paid)
	}
	if credit != 100 || balance != 0 {
		t.Errorf("creditTotal/balanceDue = %v/%v, want 100/0", credit, balance)
	}
	if status != "PAID" {
		t.Errorf("invoice status = %q, want PAID", status)
	}
}

// Over-applying must be REJECTED, never silently clamped (spec AD-9).
func TestApply_OverApplyIsRejectedNotClamped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 50)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	// Memo has 100 credit but the invoice only owes 50.
	_, err := Apply(ctx, pool, cm.ID, invUUID, 100, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("over-apply: err = %v, want ClientError", err)
	}
	// Nothing must have moved.
	_, _, credit, balance, _ := invoiceBalances(t, pool, invUUID)
	if credit != 0 || balance != 50 {
		t.Errorf("after rejected over-apply: credit/balance = %v/%v, want 0/50 (no partial write)", credit, balance)
	}
}

func TestApply_RejectsCustomerMismatch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custA := seedCustomer(t, pool)
	custB := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custA, 100)
	cm := seedApprovedMemo(t, pool, custB, 100)

	_, err := Apply(ctx, pool, cm.ID, invUUID, 50, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("cross-customer apply: err = %v, want ClientError", err)
	}
}

// Re-applying to the same invoice increments the existing live row rather than
// inserting a second (uq_cm_app_live_pair).
func TestApply_TwiceIncrementsSingleLiveRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	if _, err := Apply(ctx, pool, cm.ID, invUUID, 30, 1); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	cm, err := Apply(ctx, pool, cm.ID, invUUID, 20, 1)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(cm.Applications) != 1 {
		t.Errorf("len(Applications) = %d, want 1 live row", len(cm.Applications))
	}
	if cm.AppliedTotal != 50 {
		t.Errorf("AppliedTotal = %v, want 50", cm.AppliedTotal)
	}
	_, _, credit, balance, _ := invoiceBalances(t, pool, invUUID)
	if credit != 50 || balance != 50 {
		t.Errorf("credit/balance = %v/%v, want 50/50", credit, balance)
	}
}

func TestUnapply_ReversesAndReturnsMemoToApproved(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	cm, err := Apply(ctx, pool, cm.ID, invUUID, 100, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cm.StatusCode != "APPL" {
		t.Fatalf("precondition: StatusCode = %q, want APPL", cm.StatusCode)
	}

	cm, err = Unapply(ctx, pool, cm.ID, invUUID, 1)
	if err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if cm.StatusCode != "APPV" {
		t.Errorf("StatusCode = %q, want APPV after unapply", cm.StatusCode)
	}
	if cm.AppliedTotal != 0 || cm.UnappliedAmount != 100 {
		t.Errorf("rollup = %v/%v, want 0/100", cm.AppliedTotal, cm.UnappliedAmount)
	}
	if len(cm.Applications) != 0 {
		t.Errorf("len(Applications) = %d, want 0", len(cm.Applications))
	}

	_, _, credit, balance, status := invoiceBalances(t, pool, invUUID)
	if credit != 0 || balance != 100 {
		t.Errorf("credit/balance = %v/%v, want 0/100", credit, balance)
	}
	// Fully unapplied back to zero must walk the invoice back out of PAID.
	if status != "SENT" {
		t.Errorf("invoice status = %q, want SENT after full unapply", status)
	}
}

func TestUnapply_WithoutLiveApplicationFails(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	_, err := Unapply(ctx, pool, cm.ID, invUUID, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Errorf("unapply with no application: err = %v, want ClientError", err)
	}
}

// Voiding an APPV memo cascades: every live application is reversed and each
// affected invoice's balance is restored.
func TestTransition_VoidCascadesReversal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invA := seedSentInvoice(t, pool, custUUID, 100)
	invB := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	if _, err := Apply(ctx, pool, cm.ID, invA, 60, 1); err != nil {
		t.Fatalf("apply to A: %v", err)
	}
	if _, err := Apply(ctx, pool, cm.ID, invB, 40, 1); err != nil {
		t.Fatalf("apply to B: %v", err)
	}

	// Fully applied -> APPL, which is deliberately NOT voidable (spec AD-14).
	cmNow, err := Get(ctx, pool, cm.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cmNow.StatusCode != "APPL" {
		t.Fatalf("precondition: StatusCode = %q, want APPL", cmNow.StatusCode)
	}
	if _, err := Transition(ctx, pool, cm.ID, "VOID", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("void an APPL memo: err = %v, want ErrInvalidTransition", err)
	}

	// Unapply one to get back to APPV, then void — the cascade reverses the rest.
	if _, err := Unapply(ctx, pool, cm.ID, invB, 1); err != nil {
		t.Fatalf("unapply B: %v", err)
	}
	voided, err := Transition(ctx, pool, cm.ID, "VOID", 1)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.StatusCode != "VOID" {
		t.Errorf("StatusCode = %q, want VOID", voided.StatusCode)
	}
	if len(voided.Applications) != 0 {
		t.Errorf("len(Applications) = %d, want 0 after void cascade", len(voided.Applications))
	}

	for _, inv := range []string{invA, invB} {
		_, _, credit, balance, status := invoiceBalances(t, pool, inv)
		if credit != 0 || balance != 100 {
			t.Errorf("invoice %s: credit/balance = %v/%v, want 0/100 after void", inv, credit, balance)
		}
		if status != "SENT" {
			t.Errorf("invoice %s: status = %q, want SENT after void", inv, status)
		}
	}
}

func TestSoftDelete_BlockedWithLiveApplications(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	if _, err := Apply(ctx, pool, cm.ID, invUUID, 50, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	err := SoftDelete(ctx, pool, cm.ID, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("delete with live applications: err = %v, want ClientError", err)
	}

	// After unapplying, delete succeeds.
	if _, err := Unapply(ctx, pool, cm.ID, invUUID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if err := SoftDelete(ctx, pool, cm.ID, 1); err != nil {
		t.Fatalf("delete after unapply: %v", err)
	}
	if _, err := Get(ctx, pool, cm.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
}

// An invoice carrying live credit must not be deletable out from under the
// ledger — the mirror of creditmemo.SoftDelete's own guard.
func TestInvoiceDelete_BlockedByLiveCreditApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	if _, err := Apply(ctx, pool, cm.ID, invUUID, 50, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	err := invoice.SoftDelete(ctx, pool, invUUID, 1)
	var ice invoice.ClientError
	if !errors.As(err, &ice) {
		t.Fatalf("delete invoice with live credit: err = %v, want invoice.ClientError", err)
	}
	if !strings.Contains(err.Error(), "credit") {
		t.Errorf("message = %q, want it to mention credit memo applications", err.Error())
	}
}

// The nil-map guard: a PATCH that omits customFields must not 500. This is the
// exact bug quote/ and estimate/ both inherited by cloning.
func TestUpdate_OmittedCustomFieldsDoesNotFail(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)

	cm, err := Create(ctx, pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "credit", Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Memo: "updated memo"}, 1)
	if err != nil {
		t.Fatalf("update without customFields: %v", err)
	}
	if updated.Memo != "updated memo" {
		t.Errorf("Memo = %q, want %q", updated.Memo, "updated memo")
	}
	if updated.CustomFields == nil {
		t.Error("CustomFields is nil after update, want empty map")
	}
}

// Money is frozen once approved (spec AD-15).
func TestUpdate_MoneyImmutableAfterApproval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	cm := seedApprovedMemo(t, pool, custUUID, 100)

	_, err := Update(ctx, pool, cm.ID, UpdateCreditMemoInput{
		Lines: []CreditMemoLineInput{{LineNumber: 1, Description: "changed", Quantity: 1, UnitPrice: 999}},
	}, 1)
	var ce ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("edit lines on approved memo: err = %v, want ClientError", err)
	}

	// Non-monetary edits still go through.
	if _, err := Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Notes: "still editable"}, 1); err != nil {
		t.Errorf("non-monetary update on approved memo: %v", err)
	}

	// Draft money edits do go through.
	draft, err := Create(ctx, pool, CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "x", Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	edited, err := Update(ctx, pool, draft.ID, UpdateCreditMemoInput{
		Lines: []CreditMemoLineInput{{LineNumber: 1, Description: "y", Quantity: 2, UnitPrice: 25}},
	}, 1)
	if err != nil {
		t.Fatalf("edit draft lines: %v", err)
	}
	if edited.GrandTotal != 50 {
		t.Errorf("GrandTotal = %v, want 50 after draft line edit", edited.GrandTotal)
	}
}

// Cash and credit must compose on the same invoice without either corrupting
// the other's meaning.
func TestCreditAndCash_ComposeOnSameInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	invUUID := seedSentInvoice(t, pool, custUUID, 100)
	cm := seedApprovedMemo(t, pool, custUUID, 40)

	if _, err := Apply(ctx, pool, cm.ID, invUUID, 40, 1); err != nil {
		t.Fatalf("apply credit: %v", err)
	}
	_, paid, credit, balance, status := invoiceBalances(t, pool, invUUID)
	if paid != 0 || credit != 40 || balance != 60 {
		t.Fatalf("after credit: paid/credit/balance = %v/%v/%v, want 0/40/60", paid, credit, balance)
	}
	if status != "PART" {
		t.Fatalf("after credit: status = %q, want PART", status)
	}
}

func TestGet_NotFound(t *testing.T) {
	pool := testPool(t)
	_, err := Get(context.Background(), pool, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrNotFound", err)
	}
}
