package inventorytransfer

import "stonesuite-backend/docflow"

// Status codes, seeded for record type ITRF and adopted verbatim.
const (
	StatusDraft     = "DRFT"
	StatusPending   = "PAPV" // pending approval
	StatusApproved  = "APPV"
	StatusInTransit = "TRNS"
	StatusReceived  = "RCVD"
	StatusCancelled = "CANC"
)

const recordTypeCode = "ITRF"

// machine is the document's status graph.
//
// TRNS -> RCVD is the ONLY move out of in-transit; cancel is deliberately not
// reachable once the truck has left.
//
// The reason is the ledger, not squeamishness. Shipping writes a 'transferred'
// row keyed (record type, line, warehouse) by
// uq_inventory_ledger_src_line_transferred. A cancellation would have to put
// the stock back at the SOURCE, which is the same key the departure row already
// occupies, so the return leg would be rejected as a duplicate — leaving stock
// deducted from the source, never added anywhere, and a document saying
// "cancelled" to explain it. That is precisely the silent divergence the
// once-only index exists to prevent.
//
// KNOWN LIMITATION: a truck that turns around therefore has to be received at
// the destination and sent back on a second transfer. Modelling the U-turn
// properly needs a leg discriminator on the ledger, which is a schema change
// and not worth making on a case nobody has reported yet.
var machine = docflow.Machine{
	StatusDraft:     {StatusPending: true, StatusCancelled: true},
	StatusPending:   {StatusApproved: true, StatusDraft: true, StatusCancelled: true},
	StatusApproved:  {StatusInTransit: true, StatusDraft: true, StatusCancelled: true},
	StatusInTransit: {StatusReceived: true},
	StatusReceived:  {},
	StatusCancelled: {},
}

// Machine exposes the status graph for the controller's "what next" read.
func Machine() docflow.Machine { return machine }

// IsEditable reports whether the header and lines may still change. Approval
// freezes the document: the quantities signed off must be the ones that ship.
func IsEditable(statusCode string) bool { return statusCode == StatusDraft }

// HasShipped reports whether stock has already left the source warehouse.
func HasShipped(statusCode string) bool {
	return statusCode == StatusInTransit || statusCode == StatusReceived
}
