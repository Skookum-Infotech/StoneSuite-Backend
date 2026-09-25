//go:build dbtest

package creditmemo

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/payment"
)

// seedOverpayment creates a payment for custUUID with nothing applied, so its
// whole amount sits on it as a refundable overpayment.
func seedOverpayment(t *testing.T, pool *pgxpool.Pool, custUUID string, amount float64) string {
	t.Helper()
	var methodID int
	if err := pool.QueryRow(context.Background(),
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'`).Scan(&methodID); err != nil {
		t.Fatalf("resolve payment method: %v", err)
	}
	p, err := payment.Create(context.Background(), pool, payment.CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: amount,
	}, 1)
	if err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return p.ID
}

func paymentCreditedTotal(t *testing.T, pool *pgxpool.Pool, paymentUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(),
		`SELECT payment_credited_total FROM payment WHERE payment_uuid = $1`, paymentUUID).Scan(&v); err != nil {
		t.Fatalf("read payment_credited_total: %v", err)
	}
	return v
}

func memoFromPayment(pool *pgxpool.Pool, custUUID, paymentUUID string, amount float64) (*CreditMemo, error) {
	return Create(context.Background(), pool, CreateCreditMemoInput{
		CustomerUUID: custUUID, SourcePaymentUUID: paymentUUID, Amount: amount, Reason: "Excess payment",
	}, 1)
}

func TestCreate_FromPayment_ConsumesTheOverpayment(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)
	paymentUUID := seedOverpayment(t, pool, custUUID, 500)

	first, err := memoFromPayment(pool, custUUID, paymentUUID, 200)
	if err != nil {
		t.Fatalf("first memo: %v", err)
	}
	if first.SourcePayment == nil || first.SourcePayment.ID != paymentUUID {
		t.Fatalf("SourcePayment = %+v, want the payment %s", first.SourcePayment, paymentUUID)
	}
	if first.SourcePayment.Number == "" {
		t.Errorf("SourcePayment.Number is empty; the memo page needs it for the link label")
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 200 {
		t.Fatalf("credited after first memo = %v, want 200", got)
	}

	if _, err := memoFromPayment(pool, custUUID, paymentUUID, 300); err != nil {
		t.Fatalf("second memo (uses the rest): %v", err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 500 {
		t.Fatalf("credited after second memo = %v, want 500", got)
	}

	_, err = memoFromPayment(pool, custUUID, paymentUUID, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("a memo beyond the overpayment must be a ClientError, got %T: %v", err, err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 500 {
		t.Fatalf("a rejected memo must not change the rollup: credited = %v, want 500", got)
	}
}

func TestCreate_FromPayment_CountsSalesTaxInTheConsumedTotal(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)
	paymentUUID := seedOverpayment(t, pool, custUUID, 100)

	_, err := Create(context.Background(), pool, CreateCreditMemoInput{
		CustomerUUID: custUUID, SourcePaymentUUID: paymentUUID, Amount: 100, SalesTaxPercent: 10,
	}, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("a 110 total against a 100 overpayment must be a ClientError, got %T: %v", err, err)
	}
}

func TestCreate_FromPayment_RejectsAnotherCustomersPaymentAndAVoidedOne(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	otherCustomerPayment := seedOverpayment(t, pool, seedCustomer(t, pool), 500)
	voidedPayment := seedOverpayment(t, pool, custUUID, 500)
	if _, err := payment.Transition(ctx, pool, voidedPayment, "VOID", 1); err != nil {
		t.Fatalf("void payment: %v", err)
	}

	for name, paymentUUID := range map[string]string{
		"another customer's payment": otherCustomerPayment,
		"a voided payment":           voidedPayment,
		"an unknown payment":         "00000000-0000-0000-0000-000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := memoFromPayment(pool, custUUID, paymentUUID, 10)
			var clientErr ClientError
			if !errors.As(err, &clientErr) {
				t.Fatalf("expected a ClientError, got %T: %v", err, err)
			}
		})
	}
}

func TestTransition_Void_ReleasesTheConsumedOverpayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	paymentUUID := seedOverpayment(t, pool, custUUID, 500)
	cm, err := memoFromPayment(pool, custUUID, paymentUUID, 200)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := Transition(ctx, pool, cm.ID, "VOID", 1); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 0 {
		t.Fatalf("credited after void = %v, want 0", got)
	}
	if _, err := memoFromPayment(pool, custUUID, paymentUUID, 500); err != nil {
		t.Fatalf("the released overpayment must be usable again: %v", err)
	}
}

func TestSoftDelete_ReleasesTheConsumedOverpayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	paymentUUID := seedOverpayment(t, pool, custUUID, 500)
	cm, err := memoFromPayment(pool, custUUID, paymentUUID, 200)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := SoftDelete(ctx, pool, cm.ID, 1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 0 {
		t.Fatalf("credited after delete = %v, want 0", got)
	}
}

func TestUpdate_FromPayment_AmountChangeRechecksAvailability(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	paymentUUID := seedOverpayment(t, pool, custUUID, 500)
	cm, err := memoFromPayment(pool, custUUID, paymentUUID, 200)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cm, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Amount: floatPtr(450)}, 1)
	if err != nil {
		t.Fatalf("raise within the overpayment: %v", err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 450 {
		t.Fatalf("credited after raising = %v, want 450", got)
	}

	_, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Amount: floatPtr(600)}, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("raising past the overpayment must be a ClientError, got %T: %v", err, err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 450 {
		t.Fatalf("a rejected edit must not change the rollup: credited = %v, want 450", got)
	}
	reloaded, err := Get(ctx, pool, cm.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Subtotal != 450 {
		t.Fatalf("a rejected edit must not change the memo: subtotal = %v, want 450", reloaded.Subtotal)
	}

	cm, err = Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Amount: floatPtr(100)}, 1)
	if err != nil {
		t.Fatalf("lowering: %v", err)
	}
	if got := paymentCreditedTotal(t, pool, paymentUUID); got != 100 {
		t.Fatalf("credited after lowering = %v, want 100", got)
	}
}
