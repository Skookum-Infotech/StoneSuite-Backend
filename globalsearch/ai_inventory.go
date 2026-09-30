package globalsearch

import (
	"context"
	"fmt"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/chartofaccounts"
	"stonesuite-backend/fabrication"
	"stonesuite-backend/inventory"
	"stonesuite-backend/inventoryadjustment"
	"stonesuite-backend/inventorycount"
	"stonesuite-backend/inventorytransfer"
)

const (
	aiDateLayout   = "2006-01-02"
	aiStatusActive = "active"
	aiStatusInact  = "inactive"
)

// aiPriorityInventory ranks what a stock question most often names.
var aiPriorityInventory = []string{"sku", "name", "status", "quantity_on_hand", "unit_price"}

// aiPriorityStockDoc ranks what a stock-document question most often names.
var aiPriorityStockDoc = []string{"number", "status", "date", "location"}

var _ = addAI("inventory_item", AIHooks{
	Label: "inventory item",
	Nouns: []string{"item", "product", "stock item"},
	Load:  loadInventoryItemAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "inventory_item", "inventory_item_deleted_at", "")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "inventory_item", "inventory_item_uuid", "inventory_item_updated_at", "inventory_item_deleted_at")
	},
})

func loadInventoryItemAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	it, err := inventory.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	status := aiStatusActive
	if !it.IsActive {
		status = aiStatusInact
	}
	var onHand float64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(s.quantity_on_hand), 0)
		FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		WHERE ii.inventory_item_uuid = $1`, id).Scan(&onHand); err != nil {
		return AIRecord{}, fmt.Errorf("inventory item on hand: %w", err)
	}
	core := map[string]any{
		"sku":              it.SKU,
		"name":             it.Name,
		"status":           status,
		"description":      it.Description,
		"unit_price":       it.UnitPrice,
		"quantity_on_hand": onHand,
		"tracking":         it.Tracking,
		"barcode":          it.Barcode,
	}
	return AIRecord{Doc: rag.RecordDoc{WorkflowKey: "inventory_item", StateName: status, Core: core, Priority: aiPriorityInventory}}, nil
}

var _ = addAI("inventory_unit", AIHooks{
	Label: "inventory unit",
	Nouns: []string{"slab", "remnant", "serial number"},
	Load:  loadInventoryUnitAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "inventory_slab", "slab_deleted_at", "")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "inventory_slab", "inventory_slab_uuid", "slab_updated_at", "slab_deleted_at")
	},
})

func loadInventoryUnitAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	u, err := inventory.GetUnit(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"serial":       u.Serial,
		"status":       u.Status,
		"kind":         u.Kind,
		"form":         u.Form,
		"sku":          u.InventoryItemSKU,
		"name":         u.InventoryItemName,
		"location":     u.WarehouseName,
		"bin":          u.BinPath,
		"lot":          u.Lot,
		"block_id":     u.BlockID,
		"grade":        u.Grade,
		"finish":       u.Finish,
		"length_mm":    u.LengthMM,
		"width_mm":     u.WidthMM,
		"thickness_mm": u.ThicknessMM,
		"area":         u.Area,
	}
	return AIRecord{Doc: rag.RecordDoc{
		WorkflowKey: "inventory_unit", StateName: u.Status, Core: core,
		Priority: []string{"serial", "status", "sku", "name", "location", "area"},
	}}, nil
}

var _ = addAI("inventory_adjustment", AIHooks{
	Label: "inventory adjustment",
	Nouns: []string{"stock adjustment"},
	Load:  loadInventoryAdjustmentAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "inventory_adjustment", "adjustment_deleted_at", "")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "inventory_adjustment", "inventory_adjustment_uuid", "adjustment_updated_at", "adjustment_deleted_at")
	},
})

func loadInventoryAdjustmentAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	a, err := inventoryadjustment.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":    a.Number,
		"status":    a.StatusName,
		"date":      a.Date,
		"location":  a.WarehouseName,
		"reason":    a.ReasonName,
		"memo":      a.Notes,
		"net_delta": a.NetDelta,
		"owner":     a.OwnerName,
	}
	return AIRecord{Doc: rag.RecordDoc{WorkflowKey: "inventory_adjustment", StateName: a.StatusName, Core: core, Priority: aiPriorityStockDoc}}, nil
}

var _ = addAI("inventory_transfer", AIHooks{
	Label: "inventory transfer",
	Nouns: []string{"stock transfer", "location transfer", "warehouse transfer"},
	Load:  loadInventoryTransferAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "inventory_transfer", "transfer_deleted_at", "")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "inventory_transfer", "inventory_transfer_uuid", "transfer_updated_at", "transfer_deleted_at")
	},
})

func loadInventoryTransferAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	t, err := inventorytransfer.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":          t.Number,
		"status":          t.StatusName,
		"date":            t.Date,
		"location":        t.FromWarehouseName,
		"from_location":   t.FromWarehouseName,
		"to_location":     t.ToWarehouseName,
		"carrier":         t.Carrier,
		"tracking_number": t.TrackingNumber,
		"memo":            t.Notes,
		"total_qty":       t.TotalQty,
	}
	if t.ExpectedDate != nil {
		core["expected_date"] = *t.ExpectedDate
	}
	return AIRecord{Doc: rag.RecordDoc{WorkflowKey: "inventory_transfer", StateName: t.StatusName, Core: core, Priority: aiPriorityStockDoc}}, nil
}

var _ = addAI("inventory_count", AIHooks{
	Label: "inventory count",
	Nouns: []string{"stock count", "cycle count"},
	Load:  loadInventoryCountAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "inventory_count", "count_deleted_at", "")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "inventory_count", "inventory_count_uuid", "count_updated_at", "count_deleted_at")
	},
})

func loadInventoryCountAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	c, err := inventorycount.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":         c.Number,
		"status":         c.StatusName,
		"date":           c.Date,
		"location":       c.WarehouseName,
		"bin":            c.BinPath,
		"memo":           c.Notes,
		"line_count":     c.LineCount,
		"counted_count":  c.CountedCount,
		"variance_count": c.VarianceCount,
		"net_variance":   c.NetVariance,
	}
	return AIRecord{Doc: rag.RecordDoc{WorkflowKey: "inventory_count", StateName: c.StatusName, Core: core, Priority: aiPriorityStockDoc}}, nil
}

var _ = addAI("chart_of_account", AIHooks{
	Label: "account",
	Nouns: []string{"chart of accounts", "gl account", "ledger account"},
	Load:  loadChartOfAccountAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "coa_account", "coa_account_deleted_at", "")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "coa_account", "coa_account_uuid", "coa_account_updated_at", "coa_account_deleted_at")
	},
})

func loadChartOfAccountAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	a, err := chartofaccounts.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	status := aiStatusActive
	if !a.IsActive {
		status = aiStatusInact
	}
	core := map[string]any{
		"number":       a.Code,
		"name":         a.Name,
		"status":       status,
		"category":     a.CategoryName,
		"sub_category": a.SubCategoryName,
		"account_type": a.Type,
		"statement":    a.BSPNL,
		"memo":         a.Description,
	}
	return AIRecord{Doc: rag.RecordDoc{
		WorkflowKey: "chart_of_account", StateName: status, Core: core,
		Priority: []string{"number", "name", "status", "category", "account_type"},
	}}, nil
}

var _ = addAI("cash_transfer", AIHooks{
	OwnScoped: true,
	Label:     "cash transfer",
	Nouns:     []string{"journal entry", "bank transfer"},
	Load:      loadCashTransferAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "cash_transfer", "cash_transfer_deleted_at", "cash_transfer_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "cash_transfer", "cash_transfer_uuid", "cash_transfer_updated_at", "cash_transfer_deleted_at")
	},
})

func loadCashTransferAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	ct, err := cashtransfer.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":       ct.Number,
		"status":       ct.StatusName,
		"date":         ct.TransferDate.Format(aiDateLayout),
		"from_account": ct.FromAccount.Name,
		"to_account":   ct.ToAccount.Name,
		"amount":       ct.Amount,
		"reference":    ct.Reference,
		"memo":         ct.Notes,
	}
	return AIRecord{
		Doc: rag.RecordDoc{
			WorkflowKey: "cash_transfer", StateName: ct.StatusName, Core: core,
			Priority: []string{"number", "status", "date", "amount", "from_account", "to_account"},
		},
		OwnerUserID: ct.OwnerUserID,
	}, nil
}

var _ = addAI("fabrication_job", AIHooks{
	OwnScoped: true,
	Label:     "fabrication job",
	Nouns:     []string{"job", "installation", "install job"},
	Load:      loadFabricationJobAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "fabrication_job", "fabrication_job_deleted_at", "job_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "fabrication_job", "fabrication_job_uuid", "fabrication_job_updated_at", "fabrication_job_deleted_at")
	},
})

func loadFabricationJobAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	j, err := fabrication.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	core := map[string]any{
		"number":              j.Number,
		"status":              j.Status,
		"customer":            j.Customer.Name,
		"due_date":            j.PromisedInstallDate,
		"template_date":       j.TemplateDate,
		"fabrication_start":   j.FabricationStart,
		"actual_install_date": j.ActualInstallDate,
		"memo":                j.Notes,
		"approval_status":     j.ApprovalStatus,
	}
	return AIRecord{
		Doc: rag.RecordDoc{
			WorkflowKey: "fabrication_job", StateName: j.Status, Core: core,
			Priority: []string{"number", "status", "customer", "due_date"},
		},
		OwnerUserID: j.OwnerUserID,
	}, nil
}
