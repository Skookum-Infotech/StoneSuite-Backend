// vendorbill/store_lineage.go — the optional purchase-order link on a manually
// created vendor bill. Split from store_create.go for the 300-line file cap.
package vendorbill

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/workflow"
)

// errUnknownPurchaseOrder is the one message for a purchase order that is
// malformed, missing, deleted -- or, at the controller, out of the caller's
// scope -- so a caller cannot tell those apart and probe for existence.
const errUnknownPurchaseOrder = "Unknown or deleted purchase order."

// uuidPattern matches the canonical 8-4-4-4-12 hex form. google/uuid is not a
// dependency of this module (chartofaccounts hand-rolls the same check).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// WellFormedUUID reports whether s is a syntactically valid UUID. Checked
// before querying so a malformed client string becomes a 400 ClientError
// instead of a 22P02 the controller can only render as 500.
func WellFormedUUID(s string) bool { return uuidPattern.MatchString(s) }

// UnknownPurchaseOrderError is the ClientError for a purchase order the caller
// may not link, shared by the store and the controller's scope guard.
func UnknownPurchaseOrderError() error { return ClientError{Msg: errUnknownPurchaseOrder} }

// checkLineageVendor enforces that a linked purchase order was placed with the
// bill's own vendor -- billing one vendor against another's order is always a
// data-entry mistake, and mirrors creditmemo's same-customer lineage check.
func checkLineageVendor(poVendorID, billVendorID int) error {
	if poVendorID != billVendorID {
		return ClientError{Msg: "Purchase order belongs to a different vendor than the vendor bill."}
	}
	return nil
}

// resolvePurchaseOrderLineage resolves the optional purchaseOrderUuid on a new
// bill to the purchase order's internal id. A blank uuid means "no link" and
// returns nil. The order must be live and placed with vendorID; its status is
// deliberately unrestricted -- a bill can arrive before goods do.
func resolvePurchaseOrderLineage(ctx context.Context, q workflow.Querier, poUUID string, vendorID int) (*int, error) {
	poUUID = strings.TrimSpace(poUUID)
	if poUUID == "" {
		return nil, nil
	}
	if !WellFormedUUID(poUUID) {
		return nil, UnknownPurchaseOrderError()
	}
	var id, poVendorID int
	err := q.QueryRow(ctx, `
		SELECT purchase_order_id, purchase_order_vendor_id
		FROM purchase_order
		WHERE purchase_order_uuid = $1 AND purchase_order_deleted_at IS NULL`, poUUID).Scan(&id, &poVendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, UnknownPurchaseOrderError()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve linked purchase order: %w", err)
	}
	if err := checkLineageVendor(poVendorID, vendorID); err != nil {
		return nil, err
	}
	return &id, nil
}
