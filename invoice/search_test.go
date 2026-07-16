//go:build dbtest

package invoice

import (
	"context"
	"testing"

	"stonesuite-backend/query"
)

func TestSearch_PaginationAndFilter(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	// Create a few invoices
	for i := 0; i < 3; i++ {
		_, err := Create(ctx, pool, CreateInvoiceInput{
			CustomerUUID: custUUID,
			Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
		}, 1)
		if err != nil {
			t.Fatalf("create for search: %v", err)
		}
	}

	page, err := Search(ctx, pool, "all", "test-identity", query.Request{
		Sort:  []query.SortKey{{Field: "created_at", Dir: query.DirDesc}},
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(page.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(page.Records))
	}
	if !page.HasMore {
		t.Fatal("expected HasMore = true")
	}
	if page.NextCursor == "" {
		t.Fatal("expected a NextCursor")
	}

	// Fetch next page
	page2, err := Search(ctx, pool, "all", "test-identity", query.Request{
		Sort:   []query.SortKey{{Field: "created_at", Dir: query.DirDesc}},
		Limit:  2,
		Cursor: page.NextCursor,
	})
	if err != nil {
		t.Fatalf("search page 2: %v", err)
	}

	if len(page2.Records) == 0 {
		t.Fatalf("expected at least 1 record on page 2")
	}
}

func TestSearch_GlobalSearch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		PONumber:     "UNIQUE-PO-123",
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create for global search: %v", err)
	}

	page, err := Search(ctx, pool, "all", "test-identity", query.Request{
		Search: "UNIQUE-PO-123",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	found := false
	for _, r := range page.Records {
		if r.ID == inv.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected global search to find invoice %s by PO number", inv.ID)
	}
}
