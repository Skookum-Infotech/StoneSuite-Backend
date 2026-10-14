package bintransfer

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this bin transfer's current status")

// ErrApprovalRequired maps to HTTP 409.
var ErrApprovalRequired = errors.New("this bin transfer must be approved before it can leave its current status")

// ErrApprovalNotRequired maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this bin transfer's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Bin Transfer
// (workflows.key "bin_transfer") once, so callers don't repeat the lookup and its
// panic guard. A missing entry is a programming error, caught by the registry test.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("bin_transfer")
	if !ok {
		panic("approvalchain: \"bin_transfer\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off at the Pending Approval gate via the
// shared engine. Once every configured approver has signed -- or a super admin
// overrides -- the transfer auto-advances to Approved in the same call. It does
// NOT move any slab: approval authorises, Complete moves.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*BinTransfer, error) {
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

// GetApprovalInfo resolves who is configured to sign off on a transfer's current
// gate, who already has, and whether the caller can approve or reject it.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (approvalchain.ApprovalInfo, error) {
	info, err := approvalchain.GetInfo(ctx, pool, moduleConfig(), uuid, callerEmployeeID, callerIsSuperAdmin)
	if errors.Is(err, approvalchain.ErrNotFound) {
		return approvalchain.ApprovalInfo{}, ErrNotFound
	}
	return info, err
}

// Reject vetoes a transfer awaiting approval on behalf of a configured approver
// (or a super admin): it returns to Draft with the reason kept, this round's
// sign-offs are cleared, and its owner is told. One rejection is enough -- it is
// a veto, not a vote. Editing and resubmitting starts a fresh round.
func Reject(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int, callerIsSuperAdmin bool, reason string) (*BinTransfer, error) {
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
