package itemreceipt

// tolerance.go — AD-3: how much over-delivery is accepted without an explicit
// override.
//
// The Purchase Order module originally made over-receipt unrepresentable
// (`qty_received <= quantity` as a CHECK). Receiving 102 of 100 ordered is a
// real warehouse event, so that ceiling was relaxed to non-negativity and the
// business rule lives here instead: arrivals within the tolerance post
// silently, and anything beyond it needs the item_receipt:approve grant plus a
// recorded reason.

// OverReceiptTolerancePercent is how far past the ordered quantity a line may
// be received without the item_receipt:approve override, expressed as a
// percentage of the ordered quantity.
//
// This is deliberately a single package constant rather than a per-tenant
// setting: no tenant-settings table exists yet, and inventing one for a single
// number is not worth the schema. It is the one seam to change when tenants
// need their own thresholds.
const OverReceiptTolerancePercent = 5.0

// OverReceipt describes a line that would exceed its ordered quantity, for
// error messages and the approve-gate decision.
type OverReceipt struct {
	LineNumber  int
	Ordered     float64
	AlreadyRecv float64
	Incoming    float64
}

// AllowedCeiling returns the largest cumulative received quantity a line may
// reach without an override: the ordered quantity plus the tolerance. An
// ordered quantity of zero or less has no meaningful ceiling and returns 0.
func AllowedCeiling(ordered float64) float64 {
	if ordered <= 0 {
		return 0
	}
	return ordered * (1 + OverReceiptTolerancePercent/100)
}

// WithinTolerance reports whether receiving `incoming` on top of
// `alreadyReceived` keeps a line at or under its allowed ceiling.
//
// The comparison is cumulative on purpose: three separate 40-unit receipts
// against a 100-unit order must trip the gate on the third, even though no
// single receipt exceeds the order on its own.
//
// Lines with a non-positive ordered quantity (free-text PO lines carrying no
// quantity) are never within tolerance when anything at all is received —
// there is nothing to measure the delivery against, so a human must approve it.
func WithinTolerance(ordered, alreadyReceived, incoming float64) bool {
	if incoming <= 0 {
		return true
	}
	if ordered <= 0 {
		return false
	}
	return alreadyReceived+incoming <= AllowedCeiling(ordered)
}
