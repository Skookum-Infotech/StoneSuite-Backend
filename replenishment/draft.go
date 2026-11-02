package replenishment

// draft.go — turns the buyer's per-vendor split of one item into draft
// purchase orders, through purchaseorder's own store code (never raw SQL for
// the PO itself), so numbering, tax and the approval chain behave exactly as
// they would for a buyer who built the PO by hand.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
	"stonesuite-backend/linedim"
	"stonesuite-backend/purchaseorder"
)

const (
	// MaxDraftVendors bounds how many vendors one item can be split across in a
	// single request.
	MaxDraftVendors = 10
	// maxDraftQuantity bounds one vendor's quantity, so a typo is a 400 rather
	// than a number the PO line columns cannot hold.
	maxDraftQuantity = 100000
)

// ErrNotFound is returned when the item is unknown, deleted, or not a
// stock-tracked item that can be reordered.
var ErrNotFound = errors.New("item not found")

// ClientError is a validation failure the caller can fix; the controller maps
// it to 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// VendorQty is how much of the item to order from one vendor.
type VendorQty struct {
	VendorID string  `json:"vendorId"` // external uuid
	Qty      float64 `json:"qty"`
}

// DraftInput is one item split across one or more vendors.
type DraftInput struct {
	InventoryItemID string      `json:"inventoryItemId"` // external uuid
	Vendors         []VendorQty `json:"vendors"`
}

// DraftResult is one draft purchase order that was created.
type DraftResult struct {
	VendorID        string `json:"vendorId"`
	PurchaseOrderID string `json:"purchaseOrderId"`
	ref             purchaseorder.DraftRef
}

// Ref is the created draft, for the caller to publish its notification once
// the transaction has committed.
func (r DraftResult) Ref() purchaseorder.DraftRef { return r.ref }

// draftItem is what one PO line is built from.
type draftItem struct {
	tracking      string
	dimensionUnit string
	lengthMM      float64
	widthMM       float64
	thicknessMM   float64
	avgSlabArea   float64
}

// CreateDrafts creates one draft purchase order per vendor, each with a single
// line for the item, all in one transaction: either every vendor gets its
// order or none does. The price on each line is the item's purchasing price.
func CreateDrafts(ctx context.Context, pool *pgxpool.Pool, in DraftInput, actorEmployeeID int) ([]DraftResult, error) {
	if err := validateDraftInput(in); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create reorder drafts: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	item, err := loadDraftItem(ctx, tx, in.InventoryItemID)
	if err != nil {
		return nil, err
	}

	results := make([]DraftResult, 0, len(in.Vendors))
	for _, v := range in.Vendors {
		line, err := item.line(in.InventoryItemID, v.Qty)
		if err != nil {
			return nil, err
		}
		order := purchaseorder.CreatePurchaseOrderInput{VendorUUID: strings.TrimSpace(v.VendorID)}
		order.Items = []purchaseorder.LineInput{line}

		draft, err := purchaseorder.CreateDraftTx(ctx, tx, order, actorEmployeeID)
		if err != nil {
			return nil, fmt.Errorf("create draft purchase order for vendor %s: %w", v.VendorID, err)
		}
		results = append(results, DraftResult{VendorID: v.VendorID, PurchaseOrderID: draft.ID, ref: draft})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reorder drafts: %w", err)
	}
	return results, nil
}

// validateDraftInput checks the request shape, before any database work.
func validateDraftInput(in DraftInput) error {
	if strings.TrimSpace(in.InventoryItemID) == "" {
		return ClientError{Msg: "Choose an item to order."}
	}
	if len(in.Vendors) == 0 {
		return ClientError{Msg: "Choose at least one vendor."}
	}
	if len(in.Vendors) > MaxDraftVendors {
		return ClientError{Msg: fmt.Sprintf("An item can be split across at most %d vendors.", MaxDraftVendors)}
	}
	seen := make(map[string]bool, len(in.Vendors))
	for _, v := range in.Vendors {
		id := strings.TrimSpace(v.VendorID)
		if id == "" {
			return ClientError{Msg: "Choose a vendor for every line."}
		}
		if seen[id] {
			return ClientError{Msg: "Each vendor can only appear once for an item."}
		}
		seen[id] = true
		if !(v.Qty > 0) || v.Qty > maxDraftQuantity { // negated form also rejects NaN
			return ClientError{Msg: fmt.Sprintf("Enter a quantity between 1 and %d for every vendor.", maxDraftQuantity)}
		}
	}
	return nil
}

// loadDraftItem reads what the PO line needs from the catalogue. A malformed
// uuid compares as text, so it is "not found" rather than a cast error.
func loadDraftItem(ctx context.Context, tx pgx.Tx, itemUUID string) (draftItem, error) {
	var d draftItem
	err := tx.QueryRow(ctx, `
		SELECT ii.inventory_item_tracking, ii.inventory_item_dimension_unit,
		       ii.inventory_item_length_mm, ii.inventory_item_width_mm, ii.inventory_item_thickness_mm,
		       COALESCE((SELECT AVG(s.slab_area) FROM inventory_slab s
		                 WHERE s.inventory_item_id = ii.inventory_item_id
		                   AND s.slab_form = 'full' AND s.slab_deleted_at IS NULL), 0)
		FROM inventory_item ii
		WHERE ii.inventory_item_uuid::text = $1
		  AND ii.inventory_item_deleted_at IS NULL
		  AND ii.inventory_item_is_active AND ii.inventory_item_track_stock`,
		strings.TrimSpace(itemUUID)).Scan(
		&d.tracking, &d.dimensionUnit, &d.lengthMM, &d.widthMM, &d.thicknessMM, &d.avgSlabArea)
	if errors.Is(err, pgx.ErrNoRows) {
		return draftItem{}, ErrNotFound
	}
	if err != nil {
		return draftItem{}, fmt.Errorf("load reorder item: %w", err)
	}
	return d, nil
}

// line builds the single PO line for qty of the item.
//
// A quantity-tracked item orders qty of its own unit. A slab-tracked item
// orders qty SLABS, and the line says so the way a hand-built one does: the
// slab count is the line's expected slabs, and when the item states its
// standard slab size the line carries it, which makes the line's quantity the
// area (length x width x slabs) instead of a number typed in. Without a stated
// size the quantity falls back to the slabs times the item's average slab area
// on record, and to the bare slab count when it has none.
func (d draftItem) line(itemUUID string, qty float64) (purchaseorder.LineInput, error) {
	line := purchaseorder.LineInput{LineNumber: 1, InventoryItemUUID: itemUUID}
	if d.tracking != inventory.TrackingSerialized {
		line.Quantity = qty
		return line, nil
	}
	if qty != math.Trunc(qty) {
		return purchaseorder.LineInput{}, ClientError{Msg: "Enter a whole number of slabs."}
	}
	slabs := int(qty)
	line.ExpectedSlabs = &slabs

	if d.lengthMM > 0 && d.widthMM > 0 {
		perUnit, ok := linedim.ToMM(1, d.dimensionUnit)
		if !ok || perUnit <= 0 {
			return purchaseorder.LineInput{}, fmt.Errorf("item has an unknown dimension unit %q", d.dimensionUnit)
		}
		length, width := inUnit(d.lengthMM, perUnit), inUnit(d.widthMM, perUnit)
		line.Length, line.Width = &length, &width
		if d.thicknessMM > 0 {
			thickness := inUnit(d.thicknessMM, perUnit)
			line.Thickness = &thickness
		}
		line.DimensionUnit = d.dimensionUnit
		return line, nil
	}

	line.Quantity = qty
	if d.avgSlabArea > 0 {
		line.Quantity = math.Round(qty*d.avgSlabArea*100) / 100
	}
	return line, nil
}

// inUnit converts millimetres to a unit that is perUnit millimetres long,
// rounded to the two decimals a line stores.
func inUnit(mm, perUnit float64) float64 { return math.Round(mm/perUnit*100) / 100 }
