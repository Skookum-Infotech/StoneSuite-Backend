//go:build dbtest

package payment

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

func TestUpdate_NonMonetaryFieldsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: methodID, ReferenceNumber: "Check #99", Memo: "updated"}, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ReferenceNumber != "Check #99" || updated.Amount != 100 {
		t.Fatalf("expected reference updated and amount unchanged, got %+v", updated)
	}
}

func TestSoftDelete_BlockedWithLiveApplications(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 50, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := SoftDelete(ctx, pool, p.ID, 1); err == nil {
		t.Fatal("expected delete to be blocked while a live application exists")
	}
	if _, err := Unapply(ctx, pool, p.ID, invUUID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if err := SoftDelete(ctx, pool, p.ID, 1); err != nil {
		t.Fatalf("expected delete to succeed once unapplied: %v", err)
	}
	if _, err := Get(ctx, pool, p.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// customerInternalID resolves a customer's internal serial id from its uuid —
// the "customer_id" filter key compares against that id cast to text (see
// systemFields["customer_id"] in resolver.go), matching how
// invoice/search_test.go resolves the same filter.
func customerInternalID(t *testing.T, pool *pgxpool.Pool, custUUID string) string {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(),
		`SELECT customer_id FROM customer WHERE customer_uuid = $1`, custUUID).Scan(&id); err != nil {
		t.Fatalf("resolve customer internal id: %v", err)
	}
	return strconv.Itoa(id)
}

func TestSearch_FilterAndSort(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	if _, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 300}, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	page, err := Search(ctx, pool, "all", "", query.Request{
		Filters: []query.Clause{{Field: "customer_id", Op: query.OpEq, Value: customerInternalID(t, pool, custUUID)}},
		Sort:    []query.SortKey{{Field: "amount", Dir: query.DirDesc}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Records) < 2 {
		t.Fatalf("expected at least 2 records, got %d", len(page.Records))
	}
	if page.Records[0].Amount < page.Records[1].Amount {
		t.Fatalf("expected DESC order by amount, got %v then %v", page.Records[0].Amount, page.Records[1].Amount)
	}
}

func TestSearch_UnknownFieldIsInvalidFilterError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, err := Search(ctx, pool, "all", "", query.Request{Filters: []query.Clause{{Field: "nope", Op: query.OpEq, Value: "x"}}})
	if _, ok := err.(*query.InvalidFilterError); !ok {
		t.Fatalf("expected *query.InvalidFilterError, got %T: %v", err, err)
	}
}
