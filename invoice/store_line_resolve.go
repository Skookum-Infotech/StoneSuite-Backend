package invoice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/kitref"
	"stonesuite-backend/linedim"
)

// resolvedLine is one line item after item/tax resolution + money calc, ready
// to insert.
type resolvedLine struct {
	in          InvoiceLineInput
	invItemID   *int
	sku         string
	itemName    string
	unitID      *int
	unitCode    string
	taxPercent  float64
	money       LineMoney
	kit         kitref.Ref // the kit the line was added from; zero for none
	dims        linedim.Dims
	itemDimUnit string // the catalog item's length/width unit, a line's default
}

// resolveLine snapshots a line's catalog data (if any) and computes its money.
func resolveLine(ctx context.Context, pool *pgxpool.Pool, in InvoiceLineInput, headerTaxPercent float64) (resolvedLine, error) {
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

	kit, kitMsg := kitref.Normalize(in.KitUUID, in.KitName)
	if kitMsg != "" {
		return resolvedLine{}, ClientError{Msg: fmt.Sprintf("Line %d: %s", in.LineNumber, kitMsg)}
	}

	// Free-text lines (no catalog item) snapshot the caller's description as
	// both the item name and the description.
	rl := resolvedLine{in: in, itemName: in.Description, kit: kit}
	if in.InventoryItemUUID != "" {
		var id int
		var sku, name, desc, unitCode, dimUnit string
		var unitID *int
		err := pool.QueryRow(ctx, `
			SELECT inventory_item_id, inventory_item_sku, inventory_item_name,
			       inventory_item_description, inventory_item_unit_id, u.unit_code,
			       i.inventory_item_dimension_unit
			FROM inventory_item i
			JOIN lkp_unit u ON u.unit_id = i.inventory_item_unit_id
			WHERE i.inventory_item_uuid = $1 AND i.inventory_item_deleted_at IS NULL`, in.InventoryItemUUID,
		).Scan(&id, &sku, &name, &desc, &unitID, &unitCode, &dimUnit)
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
		rl.itemDimUnit = dimUnit
	}

	// Length x width x slabs, when given, IS the quantity: derived here so a
	// stale client can never store one that disagrees with its dimensions.
	sized, dimMsg := linedim.Resolve(linedim.Input{Length: in.Length, Width: in.Width, Slabs: in.Slabs, Unit: unitOr(in.DimensionUnit, rl.itemDimUnit)}, rl.unitCode)
	if dimMsg != "" {
		return resolvedLine{}, ClientError{Msg: fmt.Sprintf("Line %d: %s", in.LineNumber, dimMsg)}
	}
	rl.dims = sized.Dims
	if sized.Derived {
		rl.in.Quantity = sized.Quantity
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
	rl.money = ComputeLine(LineInput{Quantity: rl.in.Quantity, UnitPrice: in.UnitPrice, DiscountPercent: in.DiscountPercent, TaxPercent: taxPercent})
	return rl, nil
}

// unitOr returns the unit a client sent, or the fallback when it sent none.
func unitOr(sent, fallback string) string {
	if strings.TrimSpace(sent) != "" {
		return sent
	}
	return fallback
}
