//go:build dbtest

package salesorder

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func dupInput(custUUID, itemUUID, po string, allow bool) CreateOrderInput {
	return CreateOrderInput{
		CustomerUUID:   custUUID,
		AllowDuplicate: allow,
		orderFields: orderFields{
			PONumber: po,
			Items:    []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
		},
	}
}

func TestCreate_ConcurrentDuplicatePO_ExactlyOneWins(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	pos := []string{"PO-0042", "po 42"} // same normalized value
	errs := make([]error, len(pos))
	var wg sync.WaitGroup
	for i := range pos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = Create(ctx, pool, dupInput(custUUID, itemUUID, pos[i], false), 1)
		}()
	}
	wg.Wait()

	var ok, dups int
	for _, err := range errs {
		var d *DuplicateDocumentError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &d):
			dups++
			if d.ExistingUUID == "" || d.ExistingNumber == "" {
				t.Errorf("duplicate error missing existing ref: %+v", d)
			}
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || dups != 1 {
		t.Fatalf("ok=%d dups=%d, want 1 and 1", ok, dups)
	}
}

func TestCreate_AllowDuplicate_BothSucceedAndFlagOverride(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	first, err := Create(ctx, pool, dupInput(custUUID, itemUUID, "PO-7", true), 1)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if first.DuplicateOverride {
		t.Error("first create must not flag an override")
	}
	second, err := Create(ctx, pool, dupInput(custUUID, itemUUID, "po7", true), 1)
	if err != nil {
		t.Fatalf("second create with AllowDuplicate: %v", err)
	}
	if !second.DuplicateOverride || second.DuplicateOfUUID != first.ID {
		t.Errorf("override flag = %v of %q, want true of %q", second.DuplicateOverride, second.DuplicateOfUUID, first.ID)
	}
}
