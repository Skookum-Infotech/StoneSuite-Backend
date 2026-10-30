package inventory

// on_order.go — slabs that exist because a purchase order was sent, before the
// stone has arrived.
//
// Sending a purchase order mints one inventory_slab per ordered slab with the
// status 'on_order'. Such a slab is a promise, not stock: no ledger row is
// written and inventory_stock is untouched, so it never counts as on hand and
// every handler that moves, cuts, bundles, counts or transfers stone refuses it.
// It becomes stock only when an item receipt that ticks it is posted
// (ReceiveOnOrderUnitTx), which flips it to 'available' and writes the one
// 'received' ledger row. If its order is cancelled or short-closed first it is
// written off without a ledger row (ScrapOnOrderForPOTx).
//
// Like the other writers in this package these take a caller-owned pgx.Tx and
// never begin or commit one.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	// MaxSlabSerialLen is the width of inventory_slab.slab_serial.
	MaxSlabSerialLen = 50

	// SlabSerialSuffixDigits is the minimum width of the running number in a
	// serial: <PO number>-<number>, zero-padded to at least this many digits.
	SlabSerialSuffixDigits = 3
	// slabSerialSuffixMaxDigits bounds how much of an existing serial's tail is
	// read back as that number.
	slabSerialSuffixMaxDigits = 6
)

// errStillOnOrder is the refusal every stone-handling path gives an On Order slab.
func errStillOnOrder(serial, verb string) error {
	return ClientError{Msg: fmt.Sprintf("Unit %s is still on order and has not arrived, so it cannot be %s.", serial, verb)}
}

// slabEverReceived reports whether a slab ever entered stock, i.e. has its
// once-only 'received' ledger row.
func slabEverReceived(ctx context.Context, q pgxQuerier, slabID int) (bool, error) {
	var received bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM inventory_slab_ledger WHERE inventory_slab_id = $1 AND event = $2)`,
		slabID, EventReceived).Scan(&received); err != nil {
		return false, fmt.Errorf("check slab ledger: %w", err)
	}
	return received, nil
}

// ---- serial numbering -------------------------------------------------------

// SlabSerialPrefix is the fixed part of every serial minted for a purchase
// order: its number and a dash.
func SlabSerialPrefix(poNumber string) (string, error) {
	po := strings.TrimSpace(poNumber)
	if po == "" {
		return "", ClientError{Msg: "The purchase order has no number yet, so slab serials cannot be assigned."}
	}
	prefix := po + "-"
	if len(prefix)+SlabSerialSuffixDigits > MaxSlabSerialLen {
		return "", ClientError{Msg: "The purchase order number is too long to build slab serials from."}
	}
	return prefix, nil
}

// SlabSerial formats the n-th serial under a prefix, e.g. PO-000012-003.
func SlabSerial(prefix string, n int) string {
	return fmt.Sprintf("%s%0*d", prefix, SlabSerialSuffixDigits, n)
}

// NextSlabNumber returns the number the next slab under prefix should take: one
// past the highest suffix ever used, counting scrapped and soft-deleted units so
// a serial is never handed out twice (a voided receipt's slabs keep theirs, and
// so do the On Order slabs of a cancelled order).
func NextSlabNumber(ctx context.Context, q pgxQuerier, prefix string) (int, error) {
	var highest int
	err := q.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(MAX(substr(slab_serial, length($1::text) + 1)::int), 0)
		FROM inventory_slab
		WHERE starts_with(slab_serial, $1::text)
		  AND substr(slab_serial, length($1::text) + 1) ~ '^[0-9]{1,%d}$'`, slabSerialSuffixMaxDigits),
		prefix).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("find highest slab serial: %w", err)
	}
	return highest + 1, nil
}

// DefaultWarehouseID is the tenant's default location, where an On Order slab
// sits until its receipt names the real one (inventory_slab.warehouse_id is
// mandatory and a slab on order has no location of its own yet).
func DefaultWarehouseID(ctx context.Context, tx pgx.Tx) (int, error) {
	id, err := defaultWarehouseID(ctx, tx)
	if IsClientError(err) {
		return 0, ClientError{Msg: "No location is set up to hold the slabs on order. Add one under Configuration → Company Info → Locations."}
	}
	return id, err
}

// ---- creating ---------------------------------------------------------------

// OnOrderParams is one purchase-order line's slabs to mint: Count identical
// slabs of one size, numbered FirstNumber upward under SerialPrefix.
type OnOrderParams struct {
	PurchaseOrderItemID int
	PONumber            string
	Item                ItemUnitInfo
	WarehouseID         int
	VendorID            *int

	LengthMM float64
	WidthMM  float64
	// ThicknessMM may be 0 when neither the order line nor the item states one;
	// the receipt that brings the slab in then has to supply it.
	ThicknessMM float64

	Count        int
	SerialPrefix string
	FirstNumber  int
}

// CreateOnOrderUnitsTx inserts the slabs and their history, and returns the
// serials it minted. It writes NO ledger row: an On Order slab is not stock.
func CreateOnOrderUnitsTx(ctx context.Context, tx pgx.Tx, p OnOrderParams, actorEmployeeID int) ([]string, error) {
	if p.Count <= 0 {
		return nil, nil
	}
	if p.LengthMM <= 0 || p.WidthMM <= 0 || p.ThicknessMM < 0 {
		return nil, ClientError{Msg: "On-order slabs need a positive length and width."}
	}
	// A count unit like SLAB would make offcut recovery produce a fractional
	// count, so slab stock must be area-denominated (same rule as ReceiveUnitTx).
	if p.Item.UnitCategory != UnitCategoryArea {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Slab items must use an area unit; %s is a %s unit.", p.Item.UnitCode, p.Item.UnitCategory)}
	}
	area, err := AreaFor(p.LengthMM, p.WidthMM, p.Item.UnitCode, p.Item.UnitCategory)
	if err != nil {
		return nil, err
	}

	serials := make([]string, p.Count)
	for i := range serials {
		serials[i] = SlabSerial(p.SerialPrefix, p.FirstNumber+i)
	}

	rows, err := tx.Query(ctx, `
		INSERT INTO inventory_slab (
			slab_serial, slab_unit_kind, slab_vendor_id, inventory_item_id, warehouse_id,
			slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id,
			slab_form, slab_status, slab_created_by, purchase_order_item_id)
		SELECT s, $2::varchar, $3::int, $4::int, $5::int,
		       $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::int,
		       'full', 'on_order', $11::int, $12::int
		FROM unnest($1::text[]) WITH ORDINALITY AS t(s, ord)
		ORDER BY ord
		RETURNING inventory_slab_id`,
		serials, UnitKindSlab, p.VendorID, p.Item.ItemID, p.WarehouseID,
		p.LengthMM, p.WidthMM, p.ThicknessMM, area, p.Item.UnitID,
		nullableInt(actorEmployeeID), p.PurchaseOrderItemID)
	if err != nil {
		return nil, mapUnitWriteErr(err, "insert on-order")
	}
	ids := make([]int, 0, p.Count)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan on-order slab: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapUnitWriteErr(err, "insert on-order")
	}

	note := "On order from purchase order " + strings.TrimSpace(p.PONumber)
	if err := writeBulkUnitHistory(ctx, tx, ids, "create", "", "on_order", note, actorEmployeeID); err != nil {
		return nil, err
	}
	return serials, nil
}

// writeBulkUnitHistory appends one history row per unit in a single statement,
// so a 500-slab order costs one round trip rather than 500.
func writeBulkUnitHistory(ctx context.Context, tx pgx.Tx, unitIDs []int, action, oldVal, newVal, note string, actorEmployeeID int) error {
	if len(unitIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_unit_history (
			inventory_slab_id, history_action, history_field,
			history_old_value, history_new_value, history_note, history_by)
		SELECT id, $2::varchar, 'status', $3::text, $4::text, $5::text, $6::int
		FROM unnest($1::int[]) AS id`,
		unitIDs, action, oldVal, newVal, note, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("write inventory unit history: %w", err)
	}
	return nil
}

// ---- arriving ---------------------------------------------------------------

// OnOrderArrival is one On Order slab arriving on a posted receipt.
type OnOrderArrival struct {
	SlabID              int
	PurchaseOrderItemID int
	WarehouseID         int
	BinID               *int
	// ThicknessMM is used only when the slab was minted without one; a thickness
	// the order line or item stated is never overridden by the receiver.
	ThicknessMM float64
	// ReceivedAt is "yyyy-mm-dd"; blank means today.
	ReceivedAt      string
	NeedsInspection bool
}

// ReceiveOnOrderUnitTx flips one On Order slab to 'available' at the receiving
// location and writes its one 'received' ledger row -- the moment it becomes
// stock. The slab keeps the serial and size it was minted with.
func ReceiveOnOrderUnitTx(ctx context.Context, tx pgx.Tx, a OnOrderArrival, actorEmployeeID int) (*ReceivedUnit, error) {
	var (
		rec         ReceivedUnit
		itemID      int
		serial      string
		status      string
		poItemID    *int
		thickness   float64
		deleted     bool
		oldBin      *int
		oldLocation int
	)
	err := tx.QueryRow(ctx, `
		SELECT inventory_slab_id, inventory_slab_uuid, inventory_item_id, slab_serial, slab_status,
		       purchase_order_item_id, slab_thickness_mm, slab_area, slab_deleted_at IS NOT NULL,
		       inventory_bin_id, warehouse_id
		FROM inventory_slab
		WHERE inventory_slab_id = $1
		FOR UPDATE`, a.SlabID).Scan(
		&rec.ID, &rec.UUID, &itemID, &serial, &status,
		&poItemID, &thickness, &rec.Area, &deleted,
		&oldBin, &oldLocation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "One of the slabs on this receipt does not exist."}
	}
	if err != nil {
		return nil, fmt.Errorf("lock on-order slab: %w", err)
	}
	if deleted || status != StatusOnOrder {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Slab %s is no longer on order (it is %s), so it cannot be received. Refresh the receipt and try again.", serial, status)}
	}
	if poItemID == nil || *poItemID != a.PurchaseOrderItemID {
		return nil, ClientError{Msg: fmt.Sprintf("Slab %s was not ordered on this purchase order line.", serial)}
	}
	if thickness <= 0 {
		thickness = a.ThicknessMM
	}
	if thickness <= 0 {
		return nil, ClientError{Msg: fmt.Sprintf("Slab %s needs a thickness before it can be received.", serial)}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET
			slab_status = $2, warehouse_id = $3, inventory_bin_id = $4, slab_thickness_mm = $5,
			slab_received_at = COALESCE(NULLIF($6,'')::date, CURRENT_DATE), slab_received_by = $7,
			inspection_status = CASE WHEN $8 THEN 'pending' ELSE 'legacy' END,
			slab_updated_at = NOW(), slab_updated_by = $7,
			slab_record_version = slab_record_version + 1
		WHERE inventory_slab_id = $1`,
		a.SlabID, StatusAvailable, a.WarehouseID, a.BinID, thickness,
		a.ReceivedAt, nullableInt(actorEmployeeID), a.NeedsInspection); err != nil {
		return nil, mapUnitWriteErr(err, "receive on-order")
	}
	rec.Serial = serial
	if err := SlabLedgerAndStock(ctx, tx, a.SlabID, itemID, a.WarehouseID,
		EventReceived, rec.Area, nil, actorEmployeeID); err != nil {
		return nil, err
	}
	note := ""
	if oldLocation != a.WarehouseID {
		note = "Received at a different location than the one it was ordered to."
	}
	if err := writeUnitHistory(ctx, tx, a.SlabID, "status_change", "status", StatusOnOrder, StatusAvailable,
		oldBin, a.BinID, nil, note, actorEmployeeID); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ---- cancelling -------------------------------------------------------------

// ScrapOnOrderForPOTx writes off every slab of a purchase order that is still
// On Order, because the order was cancelled or short-closed before they
// arrived. They never entered stock, so no ledger row is written. It returns how
// many it scrapped.
func ScrapOnOrderForPOTx(ctx context.Context, tx pgx.Tx, poInternalID int, note string, actorEmployeeID int) (int, error) {
	rows, err := tx.Query(ctx, `
		UPDATE inventory_slab s
		SET slab_status = $2, slab_updated_at = NOW(), slab_updated_by = $3,
		    slab_record_version = slab_record_version + 1
		FROM purchase_order_item poi
		WHERE poi.purchase_order_item_id = s.purchase_order_item_id
		  AND poi.purchase_order_id = $1
		  AND s.slab_status = $4 AND s.slab_deleted_at IS NULL
		RETURNING s.inventory_slab_id`,
		poInternalID, StatusScrapped, nullableInt(actorEmployeeID), StatusOnOrder)
	if err != nil {
		return 0, fmt.Errorf("scrap on-order slabs: %w", err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan scrapped on-order slab: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("scrap on-order slabs: %w", err)
	}
	if err := writeBulkUnitHistory(ctx, tx, ids, "scrap", StatusOnOrder, StatusScrapped, note, actorEmployeeID); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// ---- reading ----------------------------------------------------------------

// OnOrderSlab is one slab still On Order against a purchase order line, as the
// receiving screen lists it.
type OnOrderSlab struct {
	ID                  string  `json:"id"`
	Serial              string  `json:"serial"`
	PurchaseOrderItemID string  `json:"purchaseOrderItemId"`
	LengthMM            float64 `json:"lengthMm"`
	WidthMM             float64 `json:"widthMm"`
	// ThicknessMM is 0 when neither the order line nor the item stated one.
	ThicknessMM float64 `json:"thicknessMm"`
	Area        float64 `json:"area"`
}

// rowsQuerier is the part of a pool or transaction ListOnOrderForPO needs.
type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ListOnOrderForPO returns the slabs of a purchase order that are still On
// Order, in line then serial order.
func ListOnOrderForPO(ctx context.Context, q rowsQuerier, poUUID string) ([]OnOrderSlab, error) {
	rows, err := q.Query(ctx, `
		SELECT s.inventory_slab_uuid, s.slab_serial, poi.purchase_order_item_uuid,
		       s.slab_length_mm, s.slab_width_mm, s.slab_thickness_mm, s.slab_area
		FROM inventory_slab s
		JOIN purchase_order_item poi ON poi.purchase_order_item_id = s.purchase_order_item_id
		JOIN purchase_order po ON po.purchase_order_id = poi.purchase_order_id
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL
		  AND poi.item_deleted_at IS NULL
		  AND s.slab_status = $2 AND s.slab_deleted_at IS NULL
		ORDER BY poi.line_number, s.slab_serial`, poUUID, StatusOnOrder)
	if err != nil {
		return nil, fmt.Errorf("list on-order slabs: %w", err)
	}
	defer rows.Close()
	out := []OnOrderSlab{}
	for rows.Next() {
		var s OnOrderSlab
		if err := rows.Scan(&s.ID, &s.Serial, &s.PurchaseOrderItemID,
			&s.LengthMM, &s.WidthMM, &s.ThicknessMM, &s.Area); err != nil {
			return nil, fmt.Errorf("scan on-order slab: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
