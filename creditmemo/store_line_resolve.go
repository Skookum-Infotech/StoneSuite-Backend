package creditmemo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resolvedLine is one line item after item/tax resolution + money calc, ready
// to insert.
type resolvedLine struct {
	in         CreditMemoLineInput
	invItemID  *int
	sku        string
	itemName   string
	unitID     *int
	unitCode   string
	taxPercent float64
	money      LineMoney
}

// resolveLine snapshots a line's catalog data (if any) and computes its money.
//
// Nothing here touches inventory_stock: crediting returned goods does not
// restock them (spec AD-11 — this codebase has no inventory write path at all).
// inventory_item_id is recorded only so a future inventory module can find what
// was returned.
func resolveLine(ctx context.Context, pool *pgxpool.Pool, in CreditMemoLineInput, headerTaxPercent float64) (resolvedLine, error) {
	if in.LineNumber <= 0 {
		return resolvedLine{}, ClientError{Msg: "lineNumber must be positive."}
	}
	if in.Quantity < 0 || in.UnitPrice < 0 {
		return resolvedLine{}, ClientError{Msg: "quantity and unitPrice must be >= 0."}
	}
	if in.DiscountPercent < 0 || in.DiscountPercent > 100 {
		return resolvedLine{}, ClientError{Msg: "discountPercent must be between 0 and 100."}
	}
	if in.InventoryItemUUID == "" && strings.TrimSpace(in.Description) == "" {
		return resolvedLine{}, ClientError{Msg: "each line needs an inventoryItemUuid or a description."}
	}

	// Free-text lines (no catalog item) snapshot the caller's description as
	// both the item name and the description — item_name must be non-empty.
	rl := resolvedLine{in: in, itemName: strings.TrimSpace(in.Description)}
	if in.InventoryItemUUID != "" {
		var id int
		var sku, name, desc, unitCode string
		var unitID *int
		err := pool.QueryRow(ctx, `
			SELECT inventory_item_id, inventory_item_sku, inventory_item_name,
			       inventory_item_description, inventory_item_unit_id, u.unit_code
			FROM inventory_item i
			JOIN lkp_unit u ON u.unit_id = i.inventory_item_unit_id
			WHERE i.inventory_item_uuid = $1 AND i.inventory_item_deleted_at IS NULL`, in.InventoryItemUUID,
		).Scan(&id, &sku, &name, &desc, &unitID, &unitCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return resolvedLine{}, ClientError{Msg: "Unknown or deleted inventory item on line " + fmt.Sprint(in.LineNumber) + "."}
		}
		if err != nil {
			return resolvedLine{}, fmt.Errorf("resolve line item: %w", err)
		}
		rl.invItemID = &id
		rl.sku = sku
		// Catalog item name always wins on a catalog line; the caller's
		// description (if any) is kept as the line's description, else the
		// catalog item's own description is used.
		rl.itemName = name
		if in.Description == "" {
			rl.in.Description = desc
		}
		rl.unitID = unitID
		rl.unitCode = unitCode
	}

	taxPercent := headerTaxPercent
	if in.TaxRateID != nil {
		var pct float64
		if err := pool.QueryRow(ctx,
			`SELECT tax_rate_percent FROM lkp_tax_rate WHERE tax_rate_id = $1 AND tax_rate_deleted_at IS NULL`,
			*in.TaxRateID).Scan(&pct); err != nil {
			return resolvedLine{}, ClientError{Msg: "Unknown tax rate on line " + fmt.Sprint(in.LineNumber) + "."}
		}
		taxPercent = pct
	}
	rl.taxPercent = taxPercent
	rl.money = ComputeLine(LineInput{
		Quantity: in.Quantity, UnitPrice: in.UnitPrice,
		DiscountPercent: in.DiscountPercent, TaxPercent: taxPercent,
	})
	return rl, nil
}
