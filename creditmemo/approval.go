package creditmemo

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this credit memo's current status")

// ErrApprovalRequired maps to HTTP 409.
var ErrApprovalRequired = errors.New("this credit memo must be approved before it can leave its current status")

// ErrApprovalNotRequired maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this credit memo's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Credit
// Memo (workflows.key "credit_memo") once, so callers don't repeat the
// ForWorkflowKey lookup+panic-guard.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("credit_memo")
	if !ok {
		panic("approvalchain: \"credit_memo\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off on a credit memo at its current
// gate (DRFT, AD-8) via the shared approvalchain engine. Once every
// configured approver has signed off -- or a super admin overrides -- the
// memo auto-advances to the gate's target status in the same call.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*CreditMemo, error) {
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

// GetApprovalInfo resolves approvalchain.ApprovalInfo for a credit memo --
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

// Reject vetoes a credit memo that is awaiting approval on behalf of one of
// its configured approvers (or a super admin) via the shared approvalchain
// engine, keeping who rejected it and why. A credit memo's approval gate sits on its
// very first status, so there is nothing earlier to send it back to: it keeps
// its status with its approval flagged rejected, and editing it reopens
// approval (see Update). Unlike Approve it is a veto, not a vote: no quorum is
// needed. The reason / already-rejected errors pass through unchanged for the
// controller to map.
func Reject(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int, callerIsSuperAdmin bool, reason string) (*CreditMemo, error) {
	_, err := approvalchain.Reject(ctx, pool, moduleConfig(), uuid, actorEmployeeID, callerIsSuperAdmin, reason)
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
