package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/inventoryadjustment"
	"stonesuite-backend/inventorycount"
	"stonesuite-backend/inventorytransfer"
	"stonesuite-backend/query"
)

// inventory items and units have no owner column — RBAC is resource-level
// only (permission-gate, no "own" narrowing), so these adapters ignore the
// scope/identityID parameters.

var _ = addProvider(Provider{Key: "inventory_item", Resource: authz.ResourceInventoryItem, Search: searchInventoryItems})

func searchInventoryItems(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	page, err := inventory.Search(ctx, pool, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, it := range page.Records {
		out[i] = Result{Type: "inventory_item", ID: it.ID, Number: it.SKU, DisplayName: it.Name, UpdatedAt: it.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "inventory_unit", Resource: authz.ResourceInventoryUnit, Search: searchInventoryUnits})

func searchInventoryUnits(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	page, err := inventory.SearchUnits(ctx, pool, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, u := range page.Records {
		out[i] = Result{Type: "inventory_unit", ID: u.ID, Number: u.Serial, DisplayName: u.Serial, Subtitle: u.InventoryItemName, UpdatedAt: u.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "inventory_adjustment", Resource: authz.ResourceInventoryAdjustment, Search: searchInventoryAdjustments})

func searchInventoryAdjustments(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	page, err := inventoryadjustment.Search(ctx, pool, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, a := range page.Records {
		out[i] = Result{Type: "inventory_adjustment", ID: a.ID, Number: a.Number, DisplayName: "Adjustment " + a.Number, Subtitle: a.WarehouseName, UpdatedAt: a.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "inventory_transfer", Resource: authz.ResourceInventoryTransfer, Search: searchInventoryTransfers})

func searchInventoryTransfers(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	page, err := inventorytransfer.Search(ctx, pool, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, t := range page.Records {
		out[i] = Result{Type: "inventory_transfer", ID: t.ID, Number: t.Number, DisplayName: "Transfer " + t.Number, Subtitle: t.FromWarehouseName + " -> " + t.ToWarehouseName, UpdatedAt: t.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "inventory_count", Resource: authz.ResourceInventoryCount, Search: searchInventoryCounts})

func searchInventoryCounts(ctx context.Context, pool *pgxpool.Pool, _ authz.Scope, _, term string, cap int) ([]Result, bool, error) {
	page, err := inventorycount.Search(ctx, pool, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, c := range page.Records {
		out[i] = Result{Type: "inventory_count", ID: c.ID, Number: c.Number, DisplayName: "Count " + c.Number, Subtitle: c.WarehouseName, UpdatedAt: c.UpdatedAt}
	}
	return out, page.HasMore, nil
}
