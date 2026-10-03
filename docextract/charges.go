package docextract

import "strings"

var (
	discountWords = []string{"discount"}
	shippingWords = []string{"freight", "shipping", "handling", "fuel", "delivery", "surcharge", "misc"}
)

// applyCharges feeds in-table charge rows into Shipping / Discount when the
// header did not already carry them. Charges that map to neither are reported.
func applyCharges(h *Header, charges []Line) []string {
	var warns []string
	var ship, disc Cents
	var shipRef, discRef *Line
	for i := range charges {
		c := &charges[i]
		if !c.Amount.Found() || hasFlag(c.Flags, FlagAmountUnparsed) {
			warns = append(warns, WarnChargeIgnored+": "+c.Description.Value)
			continue
		}
		desc := strings.ToLower(c.Description.Value)
		amt := c.AmountCents
		if amt < 0 {
			amt = -amt
		}
		switch {
		case containsAny(desc, discountWords):
			disc += amt
			if discRef == nil {
				discRef = c
			}
		case containsAny(desc, shippingWords):
			ship += amt
			if shipRef == nil {
				shipRef = c
			}
		default:
			warns = append(warns, WarnChargeIgnored+": "+c.Description.Value)
		}
	}
	if shipRef != nil && !h.Shipping.Found() {
		h.Shipping = chargeField(shipRef, ship)
	}
	if discRef != nil && !h.Discount.Found() {
		h.Discount = chargeField(discRef, disc)
	}
	return warns
}

// chargeField builds a header field from a charge row.
func chargeField(ref *Line, total Cents) Field {
	f := ref.Amount
	f.Value = FormatCents(total)
	return f
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
