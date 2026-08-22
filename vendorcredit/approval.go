package vendorcredit

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this vendor credit's current status")

// ErrApprovalRequired maps to HTTP 409.
var ErrApprovalRequired = errors.New("this vendor credit must be approved before it can leave its current status")

// ErrApprovalNotRequired maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this vendor credit's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Vendor
// Credit (workflows.key "vendor_credit") once, so callers don't repeat the
// ForWorkflowKey lookup+panic-guard.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("vendor_credit")
	if !ok {
		panic("approvalchain: \"vendor_credit\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off on a vendor credit at its
// current gate (DRFT, AD-8) via the shared approvalchain engine. Once every
// configured approver has signed off -- or a super admin overrides -- the
// credit auto-advances to the gate's target status in the same call.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*VendorCredit, error) {
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

// GetApprovalInfo resolves approvalchain.ApprovalInfo for a vendor credit
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
