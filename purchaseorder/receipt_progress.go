package purchaseorder

// receipt_progress.go — the "Received" bar on the purchase order list: how far
// through receiving an order is, as one 0-100 number. List rows omit their
// lines, so the list endpoint calls FillReceivedPercent once per page rather
// than loading every order's lines.
//
// The web UI recomputes the same figure from an order's lines when it opens a
// row's progress card (lib/receivingProgress.ts), so the arithmetic here and
// there must stay identical: per-line fraction, averaged, rounded.

import (
	"context"
	"fmt"
	"math"

	"stonesuite-backend/workflow"
)

// percentScale turns a 0-1 fraction into a 0-100 percentage.
const percentScale = 100

// ReceivedPercent is the average of each line's received/ordered fraction,
// capped at 100% per line so an over-delivery on one line cannot hide a short
// one, as a whole percent. Lines can carry different units (sq ft, each), so
// quantities are never added together; each line counts equally. Lines with no
// ordered quantity are ignored; an order with none returns 0.
//
// Rounding never overstates: anything short of fully received reads at most
// 99, and anything received at all reads at least 1.
func ReceivedPercent(lines []LineReceipt) int {
	var sum float64
	counted := 0
	for _, l := range lines {
		if l.Quantity <= 0 {
			continue
		}
		counted++
		sum += math.Min(math.Max(l.QtyReceived, 0)/l.Quantity, 1)
	}
	if counted == 0 {
		return 0
	}
	avg := sum / float64(counted)
	pct := int(math.Round(avg * percentScale))
	switch {
	case avg < 1 && pct >= percentScale:
		return percentScale - 1
	case avg > 0 && pct == 0:
		return 1
	}
	return pct
}

// FillReceivedPercent sets ReceivedPercent on every order in the slice with a
// single query for all of their lines.
func FillReceivedPercent(ctx context.Context, q workflow.Querier, orders []PurchaseOrder) error {
	if len(orders) == 0 {
		return nil
	}
	ids := make([]string, len(orders))
	for i := range orders {
		ids[i] = orders[i].ID
	}
	rows, err := q.Query(ctx, `
		SELECT po.purchase_order_uuid, poi.quantity, poi.qty_received
		FROM purchase_order_item poi
		JOIN purchase_order po ON po.purchase_order_id = poi.purchase_order_id
		WHERE po.purchase_order_uuid = ANY($1::uuid[]) AND poi.item_deleted_at IS NULL`, ids)
	if err != nil {
		return fmt.Errorf("load purchase order lines for received percent: %w", err)
	}
	defer rows.Close()
	byOrder := make(map[string][]LineReceipt, len(orders))
	for rows.Next() {
		var id string
		var l LineReceipt
		if err := rows.Scan(&id, &l.Quantity, &l.QtyReceived); err != nil {
			return fmt.Errorf("scan purchase order line for received percent: %w", err)
		}
		byOrder[id] = append(byOrder[id], l)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load purchase order lines for received percent: %w", err)
	}
	for i := range orders {
		pct := ReceivedPercent(byOrder[orders[i].ID])
		orders[i].ReceivedPercent = &pct
	}
	return nil
}
