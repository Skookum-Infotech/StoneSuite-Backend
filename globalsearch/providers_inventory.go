package globalsearch

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/bintransfer"
	"stonesuite-backend/inventory"
	"stonesuite-backend/inventoryadjustment"
	"stonesuite-backend/inventorycount"
	"stonesuite-backend/inventorytransfer"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// inventory items and units have no owner column — RBAC is resource-level
// only, so those two adapters ignore scope. Adjustments, transfers and counts
// carry an owner and honour an "own" read grant.

var _ = addProvider(Provider{Key: "inventory_item", Resource: authz.ResourceInventoryItem, Domain: "inventory", Module: "item", Search: searchInventoryItems})

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

var _ = addProvider(Provider{Key: "inventory_unit", Resource: authz.ResourceInventoryUnit, Domain: "inventory", Module: "unit", Search: searchInventoryUnits})

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

var _ = addProvider(Provider{Key: "inventory_adjustment", Resource: authz.ResourceInventoryAdjustment, Domain: "inventory", Module: "adjustment", Search: searchInventoryAdjustments})

func searchInventoryAdjustments(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	owner, ok := inventoryOwner(ctx, pool, scope, identityID)
	if !ok {
		return nil, false, nil
	}
	page, err := inventoryadjustment.SearchOwned(ctx, pool, owner, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, a := range page.Records {
		out[i] = Result{Type: "inventory_adjustment", ID: a.ID, Number: a.Number, DisplayName: "Adjustment " + a.Number, Subtitle: a.WarehouseName, UpdatedAt: a.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "inventory_transfer", Resource: authz.ResourceInventoryTransfer, Domain: "inventory", Module: "transfer", Search: searchInventoryTransfers})

func searchInventoryTransfers(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	owner, ok := inventoryOwner(ctx, pool, scope, identityID)
	if !ok {
		return nil, false, nil
	}
	page, err := inventorytransfer.SearchOwned(ctx, pool, owner, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, t := range page.Records {
		out[i] = Result{Type: "inventory_transfer", ID: t.ID, Number: t.Number, DisplayName: "Transfer " + t.Number, Subtitle: t.FromWarehouseName + " -> " + t.ToWarehouseName, UpdatedAt: t.UpdatedAt}
	}
	return out, page.HasMore, nil
}

// Bin transfers carry an owner, so unlike the stock documents above they honour
// an "own" read grant. Module is the frontend route segment (/inventory/bin_transfer).
var _ = addProvider(Provider{Key: "bin_transfer", Resource: authz.ResourceBinTransfer, Domain: "inventory", Module: "bin_transfer", Search: searchBinTransfers})

func searchBinTransfers(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	page, err := bintransfer.Search(ctx, pool, string(scope), identityID, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, bt := range page.Records {
		out[i] = Result{Type: "bin_transfer", ID: bt.ID, Number: bt.Number, DisplayName: "Bin Transfer " + bt.Number, Subtitle: bt.WarehouseName, UpdatedAt: bt.UpdatedAt}
	}
	return out, page.HasMore, nil
}

var _ = addProvider(Provider{Key: "inventory_count", Resource: authz.ResourceInventoryCount, Domain: "inventory", Module: "count", Search: searchInventoryCounts})

func searchInventoryCounts(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, term string, cap int) ([]Result, bool, error) {
	owner, ok := inventoryOwner(ctx, pool, scope, identityID)
	if !ok {
		return nil, false, nil
	}
	page, err := inventorycount.SearchOwned(ctx, pool, owner, query.Request{Search: term, Limit: cap})
	if err != nil {
		return nil, false, err
	}
	out := make([]Result, len(page.Records))
	for i, c := range page.Records {
		out[i] = Result{Type: "inventory_count", ID: c.ID, Number: c.Number, DisplayName: "Count " + c.Number, Subtitle: c.WarehouseName, UpdatedAt: c.UpdatedAt}
	}
	return out, page.HasMore, nil
}

// inventoryOwner resolves the owner-employee narrowing for an own-scope caller:
// nil for scope=all, the caller's employee id otherwise. ok=false means an
// own-scoped caller with no employee row, who owns nothing (fail closed).
func inventoryOwner(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (owner *int, ok bool) {
	if scope == authz.ScopeAll {
		return nil, true
	}
	id, found := workflow.EmployeeIDByIdentity(ctx, pool, identityID)
	if !found {
		return nil, false
	}
	return &id, true
}
