// vendorbill/approval.go — AD-6: the configuration-driven approval gate,
// delegating to the shared approvalchain engine (see approvalchain/engine.go).
package vendorbill

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the vendor bill's current status tries to approve it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this vendor bill's current status")

// ErrApprovalRequired is returned when a vendor bill is asked to leave a
// status that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this vendor bill must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// vendor bill whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this vendor bill's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Vendor
// Bill (workflows.key "vendor_bill") once, so callers don't repeat the
// ForWorkflowKey lookup+panic-guard.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("vendor_bill")
	if !ok {
		panic("approvalchain: \"vendor_bill\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off on a vendor bill at its current
// gate (PAPV, AD-6) via the shared approvalchain engine. Once every
// configured approver has signed off -- or a super admin overrides -- the
// bill auto-advances to the gate's target status in the same call.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*VendorBill, error) {
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

// GetApprovalInfo resolves approvalchain.ApprovalInfo for a vendor bill --
// who is configured to sign off on its current gate, who already has, and
// whether the requesting caller can approve it -- so the detail page can
// show a banner instead of a transition control that would just 409.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (approvalchain.ApprovalInfo, error) {
	info, err := approvalchain.GetInfo(ctx, pool, moduleConfig(), uuid, callerEmployeeID, callerIsSuperAdmin)
	if errors.Is(err, approvalchain.ErrNotFound) {
		return approvalchain.ApprovalInfo{}, ErrNotFound
	}
	return info, err
}
