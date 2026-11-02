package vendortransactions

// Derived chain statuses, from the purchase order's first receipt to full payment.
const (
	ChainNotReceived       = "not_received"
	ChainPartiallyReceived = "partially_received"
	ChainReceived          = "received"
	ChainBilled            = "billed"
	ChainPaid              = "paid"
)

// Status codes the chain reads that the registry does not define.
const (
	// codePaid is the vendor bill status a fully settled bill carries.
	codePaid = "PAID"
	// codeReceived is the purchase order status once everything has arrived.
	codeReceived = "RCVD"
)

// chainBill is the part of a visible bill the derived status depends on.
type chainBill struct {
	statusCode string
	balanceDue *float64
}

// chainFacts are the inputs of deriveChainStatus. Receipts counts the receipts
// the caller may read plus those hidden by a missing grant (that count is already
// disclosed); an own-scoped caller's others' receipts never count. bills holds
// only the bills the caller may read.
type chainFacts struct {
	poCode      string
	receipts    int
	bills       []chainBill
	billsHidden bool
}

// deriveChainStatus maps the purchase order's lifecycle and the visible bills to
// one chain status. When any bill is hidden the status never exceeds received,
// so unreadable data cannot be inferred from it.
func deriveChainStatus(f chainFacts) string {
	status := ChainNotReceived
	switch {
	case f.poCode == codeReceived || (f.poCode == codeClosed && f.receipts > 0):
		status = ChainReceived
	case f.poCode == codePartial:
		status = ChainPartiallyReceived
	}
	if f.billsHidden {
		return status
	}
	live := 0
	allPaid := true
	for _, b := range f.bills {
		if b.statusCode == codeVoid {
			continue
		}
		live++
		settled := b.statusCode == codePaid || (b.balanceDue != nil && *b.balanceDue == 0)
		allPaid = allPaid && settled
	}
	switch {
	case live == 0:
		return status
	case allPaid:
		return ChainPaid
	default:
		return ChainBilled
	}
}
