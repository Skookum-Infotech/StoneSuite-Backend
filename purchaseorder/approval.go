// purchaseorder/approval.go — AD-6: the configuration-driven approval gate,
// delegating to the shared approvalchain engine (see approvalchain/engine.go).
package purchaseorder

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the purchase order's current status tries to approve it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this purchase order's current status")

// ErrApprovalRequired is returned when a purchase order is asked to leave a
// status that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this purchase order must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// purchase order whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this purchase order's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Purchase
// Order (workflows.key "purchase_order") once, so callers don't repeat the
// ForWorkflowKey lookup+panic-guard.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("purchase_order")
	if !ok {
		panic("approvalchain: \"purchase_order\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off on a purchase order at its
// current gate (PAPV, AD-6) via the shared approvalchain engine. Once every
// configured approver has signed off -- or a super admin overrides -- the
// order auto-advances to the gate's target status in the same call.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*PurchaseOrder, error) {
	_, err := approvalchain.Approve(ctx, pool, moduleConfig(), uuid, approverEmployeeID, callerIsSuperAdmin)
	switch {
	case errors.Is(err, approvalchain.ErrNotFound):
		return nil, ErrNotFound
	case errors.Is(err, approvalchain.ErrNotApprover):
		return nil, ErrNotApprover
	case errors.Is(err, approvalchain.ErrApprovalNotRequired):
		return nil, ErrApprovalNotRequired
	case err != nil:
		return nil, err
	}
	return Get(ctx, pool, uuid)
}

// GetApprovalInfo resolves approvalchain.ApprovalInfo for a purchase order
// -- who is configured to sign off on its current gate, who already has,
// and whether the requesting caller can approve it -- so the detail page
// can show a banner instead of a transition control that would just 409.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (approvalchain.ApprovalInfo, error) {
	info, err := approvalchain.GetInfo(ctx, pool, moduleConfig(), uuid, callerEmployeeID, callerIsSuperAdmin)
	if errors.Is(err, approvalchain.ErrNotFound) {
		return approvalchain.ApprovalInfo{}, ErrNotFound
	}
	return info, err
}
