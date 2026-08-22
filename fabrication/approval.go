package fabrication

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this job's current status")

// ErrApprovalRequired maps to HTTP 409.
var ErrApprovalRequired = errors.New("this job must be approved before it can leave its current status")

// ErrApprovalNotRequired maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this job's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for
// Fabrication Job (workflows.key "installation") once, so callers don't
// repeat the ForWorkflowKey lookup+panic-guard.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("installation")
	if !ok {
		panic("approvalchain: \"installation\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off on a job at its current gate
// (TAPV or QCPS, spec §2.7) via the shared approvalchain engine. Once every
// configured approver has signed off -- or a super admin overrides -- the
// job auto-advances to the gate's target status in the same call.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*Job, error) {
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

// GetApprovalInfo resolves approvalchain.ApprovalInfo for a job -- who is
// configured to sign off on its current gate, who already has, and whether
// the requesting caller can approve it -- so the detail page can show a
// banner instead of a transition control that would just 409.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (approvalchain.ApprovalInfo, error) {
	info, err := approvalchain.GetInfo(ctx, pool, moduleConfig(), uuid, callerEmployeeID, callerIsSuperAdmin)
	if errors.Is(err, approvalchain.ErrNotFound) {
		return approvalchain.ApprovalInfo{}, ErrNotFound
	}
	return info, err
}
