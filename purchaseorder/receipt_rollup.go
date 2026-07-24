package purchaseorder

// receipt_rollup.go — AD-4: derive the header receiving status from per-line
// ordered vs. received quantities. Pure; built and tested now so the future
// Item Receipt module can drive SENT → PART → RCVD automatically after it
// posts qty_received updates. Nothing in this module calls it from a public
// endpoint yet — PART/RCVD remain manually reachable via transition for
// tenants operating without receipts.

// LineReceipt is one line's ordered vs. received quantity.
type LineReceipt struct {
	Quantity    float64
	QtyReceived float64
}

// RollupReceiptStatus returns the status code the header should carry given
// its lines' receiving progress, starting from a current post-approval code:
//
//   - nothing received            → "" (no change)
//   - some received, not all      → "PART"
//   - every line fully received   → "RCVD"
//
// Lines with zero ordered quantity are ignored. An empty line set returns ""
// (no change). Callers must still gate the change through ValidateTransition
// — a DRFT order never rolls forward, and a CLSD/CANC order never reopens.
func RollupReceiptStatus(lines []LineReceipt) string {
	var any, all bool
	all = true
	seen := false
	for _, l := range lines {
		if l.Quantity <= 0 {
			continue
		}
		seen = true
		if l.QtyReceived > 0 {
			any = true
		}
		if l.QtyReceived < l.Quantity {
			all = false
		}
	}
	switch {
	case !seen || !any:
		return ""
	case all:
		return "RCVD"
	default:
		return "PART"
	}
}
