//go:build dbtest

package invoice_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/creditmemo"
	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
)

// seedSentInvoice creates a customer and a sent invoice for grandTotal against
// it, returning both uuids.
func seedSentInvoice(t *testing.T, pool *pgxpool.Pool, grandTotal float64) (custUUID, invUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custTypeID, "Void Guard Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, 'Void Guard Item', 1, $2, 1) RETURNING inventory_item_uuid`, "VOID-SKU-"+suffix, grandTotal).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: grandTotal}},
	}, 1)
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	// No approvers configured, so PAPV auto-skips to APPV (see store_delete_guard_test.go).
	for _, st := range []string{"PAPV", "SENT"} {
		if inv, err = invoice.Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition invoice to %s: %v", st, err)
		}
	}
	return custUUID, inv.ID
}

// applyPayment records a payment for amount applied inline to invUUID.
func applyPayment(t *testing.T, pool *pgxpool.Pool, custUUID, invUUID string, amount float64) string {
	t.Helper()
	ctx := context.Background()
	var methodID int
	if err := pool.QueryRow(ctx,
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'`).Scan(&methodID); err != nil {
		t.Fatalf("resolve payment method: %v", err)
	}
	p, err := payment.Create(ctx, pool, payment.CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: amount,
		Applications: []payment.ApplicationInput{{InvoiceUUID: invUUID, Amount: amount}},
	}, 1)
	if err != nil {
		t.Fatalf("create payment with inline application: %v", err)
	}
	return p.ID
}

// assertInvoiceStatus fails the test unless invUUID is currently in want.
func assertInvoiceStatus(t *testing.T, pool *pgxpool.Pool, invUUID, want string) *invoice.Invoice {
	t.Helper()
	inv, err := invoice.Get(context.Background(), pool, invUUID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if inv.StatusCode != want {
		t.Fatalf("invoice status = %q, want %q", inv.StatusCode, want)
	}
	return inv
}

func TestTransition_VoidBlockedWithLivePaymentApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	paymentUUID := applyPayment(t, pool, custUUID, invUUID, 40)

	if _, err := invoice.Transition(ctx, pool, invUUID, "VOID", 1); !errors.Is(err, invoice.ErrVoidWithSettlement) {
		t.Fatalf("voiding with a live payment application: got %v, want ErrVoidWithSettlement", err)
	}
	assertInvoiceStatus(t, pool, invUUID, "PART")

	if _, err := payment.Unapply(ctx, pool, paymentUUID, invUUID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if _, err := invoice.Transition(ctx, pool, invUUID, "VOID", 1); err != nil {
		t.Fatalf("void after unapply: %v", err)
	}
	assertInvoiceStatus(t, pool, invUUID, "VOID")
}

func TestTransition_VoidBlockedWithLiveCreditApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)

	cm, err := creditmemo.Create(ctx, pool, creditmemo.CreateCreditMemoInput{CustomerUUID: custUUID, Amount: 30, Reason: "Returned goods"}, 1)
	if err != nil {
		t.Fatalf("create credit memo: %v", err)
	}
	if _, err := creditmemo.Transition(ctx, pool, cm.ID, "APPV", 1); err != nil {
		t.Fatalf("approve credit memo: %v", err)
	}
	if _, err := creditmemo.Apply(ctx, pool, cm.ID, invUUID, 30, 1); err != nil {
		t.Fatalf("apply credit memo: %v", err)
	}

	if _, err := invoice.Transition(ctx, pool, invUUID, "VOID", 1); !errors.Is(err, invoice.ErrVoidWithSettlement) {
		t.Fatalf("voiding with a live credit application: got %v, want ErrVoidWithSettlement", err)
	}
	assertInvoiceStatus(t, pool, invUUID, "PART")

	if _, err := creditmemo.Unapply(ctx, pool, cm.ID, invUUID, 1); err != nil {
		t.Fatalf("uncredit: %v", err)
	}
	if _, err := invoice.Transition(ctx, pool, invUUID, "VOID", 1); err != nil {
		t.Fatalf("void after uncredit: %v", err)
	}
	assertInvoiceStatus(t, pool, invUUID, "VOID")
}

// An invoice voided before the transition guard existed can still carry live
// settlement. Unapplying part of it must update the rollup but leave the status
// VOID -- before the fix the remaining 40 re-derived it to PART.
func TestUnapply_LegacyVoidedInvoiceStaysVoid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	paymentUUID := applyPayment(t, pool, custUUID, invUUID, 60)
	applyPayment(t, pool, custUUID, invUUID, 40)

	// Simulate a pre-guard void by forcing the status directly.
	if _, err := pool.Exec(ctx, `
		UPDATE invoice SET invoice_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'INVC' AND rs.record_status_code = 'VOID')
		WHERE invoice_uuid = $1`, invUUID); err != nil {
		t.Fatalf("force legacy void: %v", err)
	}

	if _, err := payment.Unapply(ctx, pool, paymentUUID, invUUID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	inv := assertInvoiceStatus(t, pool, invUUID, "VOID")
	if inv.AmountPaid != 40 {
		t.Fatalf("AmountPaid = %v, want 40 (rollup still recomputed)", inv.AmountPaid)
	}
}
