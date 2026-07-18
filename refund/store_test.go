//go:build dbtest

package refund

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/creditmemo"
	"stonesuite-backend/payment"
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

// seedCustomer inserts a minimal live customer.
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

func firstMethodID(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(),
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'`).Scan(&id); err != nil {
		t.Fatalf("resolve payment method: %v", err)
	}
	return id
}

// seedUnappliedPayment creates a customer + a payment with no applications, so
// its entire amount sits in payment_unapplied_amount — the overpayment
// scenario a refund draws against.
func seedUnappliedPayment(t *testing.T, pool *pgxpool.Pool, amount float64) (custUUID, paymentUUID string) {
	t.Helper()
	ctx := context.Background()
	custUUID = seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	p, err := payment.Create(ctx, pool, payment.CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: amount,
	}, 1)
	if err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return custUUID, p.ID
}

// seedApprovedCreditMemo creates a customer + a goodwill credit memo (no
// invoice/sales-order lineage) transitioned to APPV, so its entire amount
// sits in credit_memo_unapplied_amount.
func seedApprovedCreditMemo(t *testing.T, pool *pgxpool.Pool, amount float64) (custUUID, creditMemoUUID string) {
	t.Helper()
	ctx := context.Background()
	custUUID = seedCustomer(t, pool)
	cm, err := creditmemo.Create(ctx, pool, creditmemo.CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []creditmemo.CreditMemoLineInput{{LineNumber: 1, Description: "Goodwill credit", Quantity: 1, UnitPrice: amount}},
	}, 1)
	if err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}
	cm, err = creditmemo.Transition(ctx, pool, cm.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("approve credit memo: %v", err)
	}
	return custUUID, cm.ID
}

func paymentRefundedTotal(t *testing.T, pool *pgxpool.Pool, paymentUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(),
		`SELECT payment_refunded_total FROM payment WHERE payment_uuid = $1`, paymentUUID).Scan(&v); err != nil {
		t.Fatalf("read payment_refunded_total: %v", err)
	}
	return v
}

func creditMemoRefundedTotal(t *testing.T, pool *pgxpool.Pool, creditMemoUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(),
		`SELECT credit_memo_refunded_total FROM credit_memo WHERE credit_memo_uuid = $1`, creditMemoUUID).Scan(&v); err != nil {
		t.Fatalf("read credit_memo_refunded_total: %v", err)
	}
	return v
}

func TestCreate_HeaderOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)

	rf, err := Create(ctx, pool, CreateRefundInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: 500,
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(rf.Number, "RFND-") {
		t.Fatalf("expected RFND- prefixed number, got %q", rf.Number)
	}
	if rf.StatusCode != "PEND" {
		t.Fatalf("new refund must start PEND, got %s", rf.StatusCode)
	}
	if rf.AppliedTotal != 0 || rf.UnappliedAmount != 500 {
		t.Fatalf("expected 0 applied / 500 unapplied, got applied=%v unapplied=%v", rf.AppliedTotal, rf.UnappliedAmount)
	}
}

func TestCreate_UnknownCustomer_IsClientError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	methodID := firstMethodID(t, pool)
	_, err := Create(ctx, pool, CreateRefundInput{
		CustomerUUID: "00000000-0000-0000-0000-000000000000", MethodID: methodID, Amount: 10,
	}, 1)
	if _, ok := err.(ClientError); !ok {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
}

func TestApply_RejectsWhilePending(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 50, 1); err == nil {
		t.Fatal("expected error applying a refund that is still PEND (AD-5)")
	}
}

func TestApply_FromPaymentOverpayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	rf2, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 60, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rf2.AppliedTotal != 60 || rf2.UnappliedAmount != 40 {
		t.Fatalf("expected applied=60 unapplied=40, got applied=%v unapplied=%v", rf2.AppliedTotal, rf2.UnappliedAmount)
	}
	if len(rf2.Applications) != 1 || rf2.Applications[0].PaymentID != paymentUUID {
		t.Fatalf("expected 1 application against payment %s, got %+v", paymentUUID, rf2.Applications)
	}
	if got := paymentRefundedTotal(t, pool, paymentUUID); got != 60 {
		t.Fatalf("expected payment_refunded_total=60, got %v", got)
	}
}

func TestApply_FromCreditMemo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, cmUUID := seedApprovedCreditMemo(t, pool, 80)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 80}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	rf2, err := Apply(ctx, pool, rf.ID, "", cmUUID, 80, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rf2.AppliedTotal != 80 || rf2.UnappliedAmount != 0 {
		t.Fatalf("expected applied=80 unapplied=0, got applied=%v unapplied=%v", rf2.AppliedTotal, rf2.UnappliedAmount)
	}
	if got := creditMemoRefundedTotal(t, pool, cmUUID); got != 80 {
		t.Fatalf("expected credit_memo_refunded_total=80, got %v", got)
	}
}

func TestApply_RejectsOverAmount(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Payment has 100 available but the refund itself only has 50 unapplied —
	// capped at the refund's own balance, not the source's.
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 60, 1); err == nil {
		t.Fatal("expected error applying more than the refund's own unapplied amount (50)")
	}
}

func TestApply_RejectsCrossCustomerSource(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, paymentUUID := seedUnappliedPayment(t, pool, 100) // payment belongs to its own customer
	otherCust := seedCustomer(t, pool)                   // refund belongs to a different customer
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: otherCust, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 50, 1); err == nil {
		t.Fatal("expected error applying from a payment belonging to a different customer")
	}
}

func TestApply_RejectsVoidedPaymentSource(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	if _, err := payment.Transition(ctx, pool, paymentUUID, "VOID", 1); err != nil {
		t.Fatalf("void payment: %v", err)
	}
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 50, 1); err == nil {
		t.Fatal("expected error applying from a voided payment")
	}
}

func TestApply_RejectsUnapprovedCreditMemoSource(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	cm, err := creditmemo.Create(ctx, pool, creditmemo.CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []creditmemo.CreditMemoLineInput{{LineNumber: 1, Description: "Draft credit", Quantity: 1, UnitPrice: 50}},
	}, 1) // left at DRFT — never approved
	if err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve refund: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, "", cm.ID, 50, 1); err == nil {
		t.Fatal("expected error applying from a DRFT (unapproved) credit memo")
	}
}

func TestUnapply_RestoresBalances(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 100, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := paymentRefundedTotal(t, pool, paymentUUID); got != 100 {
		t.Fatalf("expected payment_refunded_total=100 after apply, got %v", got)
	}

	rf2, err := Unapply(ctx, pool, rf.ID, paymentUUID, "", 1)
	if err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if rf2.AppliedTotal != 0 || rf2.UnappliedAmount != 100 {
		t.Fatalf("applied=%v unapplied=%v, want 0/100", rf2.AppliedTotal, rf2.UnappliedAmount)
	}
	if got := paymentRefundedTotal(t, pool, paymentUUID); got != 0 {
		t.Fatalf("expected payment_refunded_total=0 after unapply, got %v", got)
	}
}

func TestApply_ReapplyIncreasesExistingRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 40, 1); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	rf2, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 60, 1)
	if err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if len(rf2.Applications) != 1 {
		t.Fatalf("expected exactly 1 live application row (merged), got %d", len(rf2.Applications))
	}
	if rf2.Applications[0].Amount != 100 {
		t.Fatalf("expected merged application amount 100, got %v", rf2.Applications[0].Amount)
	}
}

// TestTransition_VoidCascadesBothSources composes one refund from both a
// payment and a credit memo (spec AD-6's "$30 + $20" example), then voids it,
// and verifies both sources' refunded_total rollbacks (spec AD-3/AD-7).
func TestTransition_VoidCascadesBothSources(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 60)
	// seedApprovedCreditMemo creates its own customer; move the refund lineage
	// customer to match the payment's customer by seeding the memo directly
	// against custUUID instead.
	cm, err := creditmemo.Create(ctx, pool, creditmemo.CreateCreditMemoInput{
		CustomerUUID: custUUID,
		Lines:        []creditmemo.CreditMemoLineInput{{LineNumber: 1, Description: "Goodwill credit", Quantity: 1, UnitPrice: 40}},
	}, 1)
	if err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}
	if _, err := creditmemo.Transition(ctx, pool, cm.ID, "APPV", 1); err != nil {
		t.Fatalf("approve credit memo: %v", err)
	}

	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create refund: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve refund: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 60, 1); err != nil {
		t.Fatalf("apply payment source: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, "", cm.ID, 40, 1); err != nil {
		t.Fatalf("apply credit memo source: %v", err)
	}

	rfBefore, err := Get(ctx, pool, rf.ID)
	if err != nil {
		t.Fatalf("get before void: %v", err)
	}
	if rfBefore.AppliedTotal != 100 || rfBefore.UnappliedAmount != 0 {
		t.Fatalf("expected fully applied before void, got applied=%v unapplied=%v", rfBefore.AppliedTotal, rfBefore.UnappliedAmount)
	}

	rfAfter, err := Transition(ctx, pool, rf.ID, "VOID", 1)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if rfAfter.StatusCode != "VOID" {
		t.Fatalf("expected VOID, got %s", rfAfter.StatusCode)
	}
	if rfAfter.AppliedTotal != 0 || rfAfter.UnappliedAmount != 100 {
		t.Fatalf("expected applied=0 unapplied=100 after void cascade, got applied=%v unapplied=%v", rfAfter.AppliedTotal, rfAfter.UnappliedAmount)
	}
	if got := paymentRefundedTotal(t, pool, paymentUUID); got != 0 {
		t.Fatalf("expected payment_refunded_total=0 after void cascade, got %v", got)
	}
	if got := creditMemoRefundedTotal(t, pool, cm.ID); got != 0 {
		t.Fatalf("expected credit_memo_refunded_total=0 after void cascade, got %v", got)
	}
}

func TestSoftDelete_BlockedWithLiveApplications(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, paymentUUID := seedUnappliedPayment(t, pool, 100)
	methodID := firstMethodID(t, pool)
	rf, err := Create(ctx, pool, CreateRefundInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Transition(ctx, pool, rf.ID, "APPV", 1); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := Apply(ctx, pool, rf.ID, paymentUUID, "", 100, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := SoftDelete(ctx, pool, rf.ID, 1); err == nil {
		t.Fatal("expected delete to be blocked while a live application exists")
	}
}

func TestGet_NotFound(t *testing.T) {
	pool := testPool(t)
	if _, err := Get(context.Background(), pool, "00000000-0000-0000-0000-000000000000"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
