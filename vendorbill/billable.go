// vendorbill/billable.go — the pure rules behind partial billing. Which orders
// may convert, and how much of each line a conversion bills, are decided here
// with no database so they can be table-tested; store_convert.go does the I/O.
package vendorbill

import (
	"fmt"
	"math"
)

// convertibleStatuses are the purchase order statuses a bill may be raised
// from: anything that has received goods. Partially Received is included so a
// vendor's invoice for a first delivery can be entered before the order is
// complete; Closed is included because a short-closed order still owes for
// what did arrive. (A Closed order with nothing received has nothing to bill
// -- planConversion reports that.)
var convertibleStatuses = map[string]bool{"PART": true, "RCVD": true, "CLSD": true}

// IsConvertibleStatus reports whether a purchase order in statusCode may be
// converted to a vendor bill.
func IsConvertibleStatus(statusCode string) bool { return convertibleStatuses[statusCode] }

// roundQty rounds to the 3 decimal places purchase_order_item.quantity is
// stored at, so float subtraction noise (5.1 - 2.1 = 3.0000000000000004) never
// reaches a bill line.
func roundQty(x float64) float64 { return math.Round(x*1000) / 1000 }

// billableQuantity is how much of one order line a new bill should cover: what
// has been received (accepted goods -- rejected ones never count) minus what
// earlier bills already cover, floored at zero. Received can fall below billed
// when a receipt is voided after its goods were billed; that is not negative
// billing, it simply leaves nothing more to bill.
func billableQuantity(received, alreadyBilled float64) float64 {
	if q := roundQty(received - alreadyBilled); q > 0 {
		return q
	}
	return 0
}

// planConversion picks the lines a conversion bills and sets each one's
// billQty. Lines with nothing left to bill are dropped. When no line has
// anything left it returns a ClientError saying which of the two reasons
// applies, since the remedy differs: receive goods first, or nothing more is
// owed.
func planConversion(lines []poSourceLine) ([]poSourceLine, error) {
	if len(lines) == 0 {
		return nil, ClientError{Msg: "Purchase order has no line items to convert."}
	}
	billable := make([]poSourceLine, 0, len(lines))
	anyReceived := false
	for _, l := range lines {
		if l.qtyReceived > 0 {
			anyReceived = true
		}
		if q := billableQuantity(l.qtyReceived, l.qtyBilled); q > 0 {
			l.billQty = q
			billable = append(billable, l)
		}
	}
	if len(billable) > 0 {
		return billable, nil
	}
	if !anyReceived {
		return nil, ClientError{Msg: "Nothing has been received on this purchase order yet, so there is nothing to bill."}
	}
	return nil, ClientError{Msg: "Everything received on this purchase order has already been billed."}
}

// linkedQty is one edited bill line that references a purchase order line.
type linkedQty struct {
	lineNumber int
	poItemID   int
	quantity   float64
}

// planLinkedQuantities totals the edited bill's quantity per purchase order
// line and checks each total against what is still billable to this bill:
// received minus what OTHER live non-void bills claim. source.qtyBilled counts
// every live non-void bill including this one, so this bill's current claim
// (current) is subtracted back out. Lines without a link never reach here.
// Returns the new conversion quantity per purchase order line id; a line the
// bill no longer references is absent.
func planLinkedQuantities(source []poSourceLine, current map[int]float64, linked []linkedQty) (map[int]float64, error) {
	byID := make(map[int]poSourceLine, len(source))
	for _, s := range source {
		byID[s.internalID] = s
	}
	totals := map[int]float64{}
	firstLine := map[int]int{}
	for _, l := range linked {
		if _, seen := firstLine[l.poItemID]; !seen {
			firstLine[l.poItemID] = l.lineNumber
		}
		totals[l.poItemID] = roundQty(totals[l.poItemID] + l.quantity)
	}
	for id, total := range totals {
		s, ok := byID[id]
		if !ok {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: purchase order line not found.", firstLine[id])}
		}
		others := roundQty(s.qtyBilled - current[id])
		if limit := billableQuantity(s.qtyReceived, others); total > limit {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: quantity %v exceeds the %v still billable for this purchase order line (received minus billed on other bills).",
				firstLine[id], total, limit)}
		}
	}
	return totals, nil
}
