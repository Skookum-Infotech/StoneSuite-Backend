// purchaseorder/store_create.go
package purchaseorder

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/linedim"
	"stonesuite-backend/workflow"
)

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert.
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int // internal FK, nil for free-text
	sku, name, desc string
	unitID          *int
	unitCode        string
	quantity        float64
	unitPrice       float64
	discountPercent float64
	taxRateID       *int
	taxPercent      float64
	money           LineMoney
	expectedSlabs   *int
	length          *float64
	width           *float64
	thickness       *float64
	dimUnit         string // the unit length, width and thickness are measured in; "" = not stated
}

// resolveLines validates and resolves every input line against the catalog
// (or free text), computing each line's stored money (AD-3, AD-7). headerTax
// is the header's default tax percent, used when a line has no tax rate.
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput, headerTax float64) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one line item is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each line item needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true
		// The slab count on a sized line is the existing expected-slabs field.
		dimIn := linedim.Input{Length: in.Length, Width: in.Width, Unit: in.DimensionUnit}
		if !dimIn.IsZero() {
			dimIn.Slabs = in.ExpectedSlabs
		}
		if !(in.Quantity > 0) && !dimIn.HasArea() { // negated form also rejects NaN
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero.", in.LineNumber)}
		}

		rl := resolvedLine{
			lineNumber:      in.LineNumber,
			quantity:        in.Quantity,
			unitPrice:       in.UnitPrice,
			discountPercent: in.DiscountPercent,
			taxRateID:       in.TaxRateID,
		}

		if in.InventoryItemUUID != "" {
			item, err := resolveInventoryItem(ctx, q, in.InventoryItemUUID)
			if err != nil {
				return nil, err
			}
			id := item.internalID
			rl.inventoryItemID = &id
			rl.sku, rl.name, rl.desc = item.sku, item.name, item.desc
			// Keep a caller-supplied description over the catalog default.
			if d := strings.TrimSpace(in.Description); d != "" {
				rl.desc = d
			}
			rl.unitID, rl.unitCode = item.unitID, item.unitCode
			if dimIn.Unit == "" {
				dimIn.Unit = item.dimensionUnit
			}
			if rl.unitPrice == 0 {
				rl.unitPrice = item.unitPrice
			}
			if rl.taxRateID == nil {
				rl.taxRateID = item.taxRateID
			}
			if err := validateExpectedSlabs(in.LineNumber, in.ExpectedSlabs, item.tracking, item.unitCode); err != nil {
				return nil, err
			}
			rl.expectedSlabs = in.ExpectedSlabs
		} else if in.ExpectedSlabs != nil {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: expected slabs only apply to a slab-tracked inventory item or one measured in SQFT or SQM.", in.LineNumber)}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventory item or a description is required.", in.LineNumber)}
		} else {
			// Free-text lines (no catalog item) snapshot the caller's
			// description as both the item name and the description — item_name
			// must never be left empty, or a round-tripped Update would see
			// neither an inventoryItemUuid nor a description and be rejected
			// by this same check (mirrors estimate/invoice).
			rl.name = in.Description
			rl.desc = in.Description
		}

		// Length x width x slabs, when given, IS the quantity: derived here so a
		// stale client can never store one that disagrees with its dimensions.
		if !dimIn.IsZero() {
			sized, dimMsg := linedim.Resolve(dimIn, rl.unitCode)
			if dimMsg != "" {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: %s", in.LineNumber, dimMsg)}
			}
			rl.length, rl.width, rl.dimUnit = sized.Dims.Length, sized.Dims.Width, sized.Dims.StoredUnit()
			if sized.Derived {
				rl.quantity = sized.Quantity
			}
		}
		if in.Thickness != nil {
			thickness, unit, thicknessMsg := linedim.ResolveThickness(*in.Thickness, dimIn.Unit, rl.unitCode)
			if thicknessMsg != "" {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: %s", in.LineNumber, thicknessMsg)}
			}
			rl.thickness = &thickness
			if rl.dimUnit == "" {
				rl.dimUnit = unit
			}
		}

		if err := validateLineAmounts(in.LineNumber, rl.quantity, rl.unitPrice, rl.discountPercent); err != nil {
			return nil, err
		}

		if rl.taxRateID != nil {
			pct, err := taxPercentForRate(ctx, q, *rl.taxRateID)
			if err != nil {
				return nil, err
			}
			rl.taxPercent = pct
		} else {
			rl.taxPercent = headerTax
		}

		rl.money = ComputeLine(CalcLineInput{
			Quantity: rl.quantity, UnitPrice: rl.unitPrice,
			DiscountPercent: rl.discountPercent, TaxPercent: rl.taxPercent,
		})
		if err := validateTotals(fmt.Sprintf("total of line %d", in.LineNumber), rl.money.Total); err != nil {
			return nil, err
		}
		out = append(out, rl)
	}
	return out, nil
}

// maxExpectedSlabs bounds the informational slab count so an absurd value is a
// 400 rather than a meaningless number on the order.
const maxExpectedSlabs = 10000

// validateExpectedSlabs checks a line's optional expected slab count. It is
// meaningful for a slab-tracked item and for any line measured in an area unit
// (where it is the Slabs of a length x width x slabs line), and must be a
// positive whole number.
func validateExpectedSlabs(lineNo int, expected *int, tracking, unitCode string) error {
	if expected == nil {
		return nil
	}
	if tracking != trackingSerialized && !linedim.IsAreaUnit(unitCode) {
		return ClientError{Msg: fmt.Sprintf("Line %d: expected slabs only apply to a slab-tracked inventory item or one measured in SQFT or SQM.", lineNo)}
	}
	if *expected <= 0 || *expected > maxExpectedSlabs {
		return ClientError{Msg: fmt.Sprintf("Line %d: expected slabs must be between 1 and %d.", lineNo, maxExpectedSlabs)}
	}
	return nil
}

// insertLines bulk-inserts resolved lines as purchase_order_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, poInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		dimUnit := l.dimUnit
		if dimUnit == "" {
			dimUnit = linedim.DefaultUnit
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO purchase_order_item (
				purchase_order_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by, expected_slabs, dim_length, dim_width, dim_unit, dim_thickness
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18, $19, $20,$21,$22,$23)`,
			poInternalID, l.lineNumber, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID), l.expectedSlabs, l.length, l.width, dimUnit, l.thickness,
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or tax rate was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert purchase order item: %w", err)
		}
	}
	return nil
}

// writeHistory records one purchase_order_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, poInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		poInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new purchase order (header + lines) inside one
// transaction: snapshots the vendor name (AD-2), resolves and prices every
// line (AD-3, AD-7), computes header totals, assigns the PORD number (AD-8),
// and starts the order at DRFT (AD-5).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreatePurchaseOrderInput, actorEmployeeID int) (*PurchaseOrder, error) {
	if strings.TrimSpace(in.VendorUUID) == "" {
		return nil, ClientError{Msg: "A vendor is required."}
	}
	// A retry whose first attempt already committed gets that order back
	// before any validation: the vendor may have changed since, and a replay
	// must answer like the original, not re-judge it.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create purchase order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim inside the create's transaction: a concurrent duplicate blocks
	// here until this one commits, then replays it instead of creating.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

	draft, err := CreateDraftTx(ctx, tx, in, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, draft.ID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create purchase order: %w", err)
	}
	notifyCreated(ctx, pool, draft.ID, draft.InternalID, actorEmployeeID)
	return Get(ctx, pool, draft.ID)
}
