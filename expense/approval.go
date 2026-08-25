// expense/approval.go — AD-4: the configuration-driven approval gate,
// delegating to the shared approvalchain engine (see approvalchain/engine.go),
// plus AD-5's dedicated Reject (which the engine has no equivalent of --
// most modules have no RJCT state, and rejecting is a decision in its own
// right, never a quorum vote).
package expense

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the claim's current status tries to approve it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this expense claim's current status")

// ErrApprovalRequired is returned when a claim is asked to leave a status
// that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this expense claim must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// claim whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this expense claim's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Expense
// (workflows.key "expense") once, so callers don't repeat the
// ForWorkflowKey lookup+panic-guard.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("expense")
	if !ok {
		panic("approvalchain: \"expense\" is not registered")
	}
	return cfg
}

// Approve records one approver's sign-off on an expense claim at its
// current gate (SUBM, AD-4) via the shared approvalchain engine. Once every
// configured approver has signed off -- or a super admin overrides -- the
// claim auto-advances to the gate's target status in the same call.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*Expense, error) {
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

// GetApprovalInfo resolves approvalchain.ApprovalInfo for an expense claim
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

// Reject moves a submitted expense claim directly to RJCT (spec AD-5) --
// unlike Approve, rejecting is itself the decision and never requires
// quorum: if approvers are configured for the current status, only a
// configured approver may reject (ErrNotApprover otherwise); if none are
// configured, any caller with expense:transition may. Always records the
// reason on the header and in expense_history.
func Reject(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int, reason string) (*Expense, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reject expense: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT exp.expense_id, exp.expense_status, rs.record_status_code
		FROM expense exp JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL
		FOR UPDATE OF exp`, uuid).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load expense for reject: %w", err)
	}
	if curStatusCode != "SUBM" {
		return nil, ClientError{Msg: "Only a submitted expense claim can be rejected."}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, expenseRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EXPN record type: %w", err)
	}
	approverTable := moduleConfig().ApproverTable
	required, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if required > 0 {
		ok, err := approvalchain.IsConfiguredApprover(ctx, tx, approverTable, recordTypeID, curStatusID, actorEmployeeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotApprover
		}
	}

	rjctStatusID, err := statusIDByCode(ctx, tx, recordTypeID, "RJCT")
	if err != nil {
		return nil, fmt.Errorf("resolve RJCT status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE expense SET
			expense_status = $2, expense_approval_status = $3,
			expense_approved_by = NULL, expense_rejected_by = $4, expense_rejection_reason = $5,
			expense_updated_at = NOW(),
			expense_updated_by = $4, expense_record_version = expense_record_version + 1
		WHERE expense_id = $1`,
		internalID, rjctStatusID, approvalchain.StatusNone, nullableInt(actorEmployeeID), reason); err != nil {
		return nil, fmt.Errorf("reject expense: %w", err)
	}

	writeHistory(ctx, tx, internalID, "reject", &curStatusID, &rjctStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reject expense: %w", err)
	}
	return Get(ctx, pool, uuid)
}
