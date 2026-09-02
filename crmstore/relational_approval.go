// crmstore/relational_approval.go
//
// CRM (lead/prospect/customer) approval, extracted out of relational_store.go
// to keep that file from growing further. Approval config is deliberately
// STAGE-scoped, not per-status: crm_workflow_approver is now written only as
// a record-type-level ("wildcard", crm_status_id IS NULL) set -- one approver
// set for Lead, one for Prospect, one for Customer -- mirroring how every
// relational Sales/Purchases module (see approvalchain/registry.go) has
// exactly one gate per module rather than one per status. A record becomes
// pending the moment it ENTERS a stage that has active approvers configured
// (creation, conversion, or a stage-crossing transition); moving between
// statuses inside the same stage does not re-trigger approval. Per-status
// scoping (crm_status_id NOT NULL) was supported until 2026-08-31 and is no
// longer written -- see the schema.sql migration that removed existing rows
// of that shape.
//
// The gate itself (checkTransitionGate) mirrors approvalchain/engine.go's
// CheckTransitionGate: while a record's current stage is pending or rejected
// approval, every outbound transition is blocked except the stage's own
// lost/unqualified exit (crmAlwaysAllowedExitCodes) -- rejecting a dead lead
// is leaving the process, not getting past its gate.
package crmstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in customer.customer_approval_status.
const (
	StatusNone     = "none"
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Sentinel errors specific to the CRM approval flow, beyond the shared ones
// in store.go (ErrNotApprover, ErrAlreadyApproved, ErrNoApproverConfigured,
// ErrAlreadyApprovedByYou).
var (
	// ErrLockedPendingApproval is returned by UpdateRecord when a record is
	// awaiting approval -- edits are blocked until it is approved, or until a
	// rejection reason is available for the owner to act on (see
	// UpdateRecord's rejected -> pending resubmission behavior, which is the
	// one edit a rejected record does accept).
	ErrLockedPendingApproval = errors.New("this record is awaiting approval and cannot be edited until it is approved or resubmitted")
	// ErrApprovalRequired is returned when a transition is blocked because the
	// record's current stage is pending or rejected approval.
	ErrApprovalRequired = errors.New("this record must be approved before it can leave its current status")
	// ErrNotRejectable is returned when Reject is called on a record that
	// isn't currently pending approval.
	ErrNotRejectable = errors.New("this record is not pending approval")
)

// crmAlwaysAllowedExitCodes are CRM status codes a gated record may still
// move to without approval -- marking a dead deal dead is a way OUT of the
// approval process, not a way past it. One per stage: Lead Unqualified,
// Prospect Closed Lost, Customer Closed Lost. Mirrors
// approvalchain.AlwaysAllowedExitCodes.
var crmAlwaysAllowedExitCodes = map[string]bool{"LUNQ": true, "PCLL": true, "CCLL": true}

// checkTransitionGate enforces the CRM approval gate at a transition or
// conversion. required must come from a live count (activeApproverCount),
// never the stored approval-status column alone -- see the ActiveApproverCount
// doc in approvalchain/engine.go for why: removing the last configured
// approver must un-gate records already sitting pending, not strand them.
func checkTransitionGate(required int, approvalStatus, toStatusCode string) error {
	if required > 0 && approvalStatus != StatusApproved && !crmAlwaysAllowedExitCodes[toStatusCode] {
		return ErrApprovalRequired
	}
	return nil
}

// ApproverInfo names one configured approver for display and whether they've
// signed off on the current pending round.
type ApproverInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Approved bool   `json:"approved"`
}

// ApprovalInfo tells a CRM detail page whether a record is actually gated
// right now, who is configured to sign off, who already has, and whether the
// caller can approve or reject -- returned embedded on GetRecord (see
// CRMOps.GetRecord), matching how Estimate/Quote/Sales Order return
// `approval` alongside the record.
type ApprovalInfo struct {
	// Status mirrors customer.customer_approval_status: none | pending |
	// approved | rejected.
	Status string `json:"status"`
	// Gated is computed fresh (RequiredApprovals > 0 && Status is pending or
	// rejected), NOT read off the stored status column alone -- see
	// checkTransitionGate.
	Gated bool `json:"gated"`
	// Approvers, RequiredApprovals, ApprovedCount, CanApprove, CanReject,
	// IsOverride and CallerAlreadyApproved are only meaningful while Gated is
	// true.
	Approvers         []ApproverInfo `json:"approvers"`
	RequiredApprovals int            `json:"requiredApprovals"`
	ApprovedCount     int            `json:"approvedCount"`
	CanApprove        bool           `json:"canApprove"`
	CanReject         bool           `json:"canReject"`
	// IsOverride is true when CanApprove is true only because the caller is a
	// super admin, not because they're a configured approver.
	IsOverride bool `json:"isOverride"`
	// CallerAlreadyApproved is true when the caller is a configured approver
	// who has already signed off this round.
	CallerAlreadyApproved bool `json:"callerAlreadyApproved"`
	// Rejection fields are set only while Status == "rejected".
	RejectedByName  string     `json:"rejectedByName,omitempty"`
	RejectedAt      *time.Time `json:"rejectedAt,omitempty"`
	RejectionReason string     `json:"rejectionReason,omitempty"`
}

// GetApprovalInfo resolves ApprovalInfo for a record from callerIdentityID's
// perspective. Never errors on "not gated" or "not an approver" -- those are
// just Gated/CanApprove being false, since this is a read-only UI-affordance
// check, not an action.
func (s *relationalStore) GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, id, callerIdentityID string, callerIsSuperAdmin bool) (ApprovalInfo, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return ApprovalInfo{}, err
	}
	status, _ := rec.CoreFields["approval_status"].(string)
	info := ApprovalInfo{Status: status}

	required, err := s.activeApproverCount(ctx, pool, id)
	if err != nil {
		return ApprovalInfo{}, err
	}
	if required == 0 {
		return info, nil
	}
	info.RequiredApprovals = required
	info.Gated = status == StatusPending || status == StatusRejected

	if status == StatusRejected {
		if err := pool.QueryRow(ctx, `
			SELECT c.customer_rejection_reason, c.customer_rejected_at,
			       COALESCE(TRIM(COALESCE(e.employee_first_name,'') || ' ' || COALESCE(e.employee_last_name,'')), '')
			FROM customer c
			LEFT JOIN employee e ON e.employee_id = c.customer_rejected_by
			WHERE c.customer_uuid = $1`, id,
		).Scan(&info.RejectionReason, &info.RejectedAt, &info.RejectedByName); err != nil {
			return ApprovalInfo{}, fmt.Errorf("load rejection info: %w", err)
		}
	}

	rows, err := pool.Query(ctx, `
		SELECT e.employee_id, COALESCE(NULLIF(u.full_name,''), u.email),
		       EXISTS(
		           SELECT 1 FROM customer_approval ca
		           JOIN customer c2 ON c2.customer_id = ca.customer_id
		           WHERE c2.customer_uuid = $1 AND ca.approver_employee_id = a.approver_employee_id
		       )
		FROM crm_workflow_approver a
		JOIN customer c ON c.record_type = a.record_type_id
		JOIN employee e ON e.employee_id = a.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE c.customer_uuid = $1 AND a.is_active AND a.crm_status_id IS NULL
		ORDER BY 2`, id)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load approvers: %w", err)
	}
	defer rows.Close()
	empID, found := s.employeeIDByIdentity(ctx, pool, callerIdentityID)
	var approvers []ApproverInfo
	approvedCount := 0
	callerAlreadyApproved := false
	callerIsApprover := false
	for rows.Next() {
		var a ApproverInfo
		var eid int
		if err := rows.Scan(&eid, &a.Name, &a.Approved); err != nil {
			return ApprovalInfo{}, fmt.Errorf("scan approver: %w", err)
		}
		a.ID = fmt.Sprint(eid)
		if a.Approved {
			approvedCount++
		}
		if found && eid == empID {
			callerIsApprover = true
			callerAlreadyApproved = a.Approved
		}
		approvers = append(approvers, a)
	}
	if err := rows.Err(); err != nil {
		return ApprovalInfo{}, fmt.Errorf("iterate approvers: %w", err)
	}

	info.Approvers = approvers
	info.ApprovedCount = approvedCount
	if info.Gated {
		info.CallerAlreadyApproved = callerAlreadyApproved
		info.CanApprove = callerIsApprover || callerIsSuperAdmin
		info.CanReject = status == StatusPending && (callerIsApprover || callerIsSuperAdmin)
		info.IsOverride = !callerIsApprover && callerIsSuperAdmin
	}
	return info, nil
}

// approvalDecision is the pure branching logic behind Approve: given the
// record's current approval status, the live required-approver count, and
// what's known about the caller, it decides whether the approval may proceed
// and, if so, whether it finalizes (clears) the gate. Kept side-effect-free
// so every branch is unit-testable without a database.
func approvalDecision(status string, required int, callerIsApprover, callerIsSuperAdmin, callerAlreadyApproved bool, approvalsSoFar int) (finalize bool, err error) {
	switch status {
	case StatusApproved:
		return false, ErrAlreadyApproved
	case StatusPending:
		if required == 0 {
			return false, ErrNoApproverConfigured
		}
		if !callerIsApprover && callerIsSuperAdmin {
			// Override finalizes immediately, skipping quorum entirely rather
			// than counting as one more sign-off -- logged distinctly as
			// "approve_override" by the caller.
			return true, nil
		}
		if !callerIsApprover {
			return false, ErrNotApprover
		}
		if callerAlreadyApproved {
			return false, ErrAlreadyApprovedByYou
		}
		return approvalsSoFar+1 >= required, nil
	default: // "none", "rejected" -- rejected must be resubmitted (edited) first.
		return false, ClientError{Msg: "This record is not pending approval."}
	}
}

// Approve records callerIdentityID's sign-off on a record pending approval
// and, once every configured approver has signed off (or a super admin
// overrides), finalizes it -- the gate clears but the record stays at its
// current status (see package doc: unlike Sales/Purchases, CRM approval does
// not auto-advance to a new status).
func (s *relationalStore) Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string, callerIsSuperAdmin bool) (*workflow.Record, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	status, _ := rec.CoreFields["approval_status"].(string)

	required, err := s.activeApproverCount(ctx, pool, id)
	if err != nil {
		return nil, fmt.Errorf("count active approvers: %w", err)
	}

	empID, found := s.employeeIDByIdentity(ctx, pool, approverIdentityID)
	var (
		callerIsApprover, callerAlreadyApproved bool
		approvalsSoFar                          int
	)
	if found {
		callerIsApprover, err = s.isConfiguredApprover(ctx, pool, id, empID)
		if err != nil {
			return nil, fmt.Errorf("check configured approver: %w", err)
		}
		if callerIsApprover {
			callerAlreadyApproved, err = s.hasApproved(ctx, pool, id, empID)
			if err != nil {
				return nil, fmt.Errorf("check already approved: %w", err)
			}
			approvalsSoFar, err = s.approvalCount(ctx, pool, id)
			if err != nil {
				return nil, err
			}
		}
	}

	finalize, err := approvalDecision(status, required, callerIsApprover, callerIsSuperAdmin, callerAlreadyApproved, approvalsSoFar)
	if err != nil {
		return nil, err
	}

	historyAction := "approve"
	isOverride := !callerIsApprover && callerIsSuperAdmin
	if isOverride {
		historyAction = "approve_override"
	} else {
		// Reaching here implies found == true (empID is valid): approvalDecision
		// only returns a nil error down this branch when callerIsApprover is
		// true, which requires found == true.
		if _, err := pool.Exec(ctx, `
			INSERT INTO customer_approval (customer_id, approver_employee_id)
			SELECT customer_id, $2 FROM customer WHERE customer_uuid = $1
			ON CONFLICT (customer_id, approver_employee_id) DO NOTHING`, id, empID); err != nil {
			return nil, fmt.Errorf("record approval: %w", err)
		}
	}

	if finalize {
		if _, err := pool.Exec(ctx, `
			UPDATE customer SET
				customer_is_approved = TRUE, customer_approval_status = 'approved',
				customer_approved_by = $2, customer_approved_at = NOW(),
				customer_rejected_by = NULL, customer_rejected_at = NULL, customer_rejection_reason = '',
				customer_updated_at = NOW(),
				customer_record_version = customer_record_version + 1
			WHERE customer_uuid = $1`, id, nullableInt(empID)); err != nil {
			return nil, fmt.Errorf("approve customer record: %w", err)
		}
	}
	s.writeHistory(ctx, pool, id, historyAction, empID)
	return s.GetRecord(ctx, pool, id)
}

// Reject records callerIdentityID's rejection of a record pending approval,
// with a reason. Unlike Approve, this is a veto, not a vote -- any single
// configured approver (or a super admin, logged the same as an override) may
// reject without waiting on quorum. The record stays at its current status;
// editing it is how the owner resubmits (see UpdateRecord).
func (s *relationalStore) Reject(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID, reason string, callerIsSuperAdmin bool) (*workflow.Record, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	status, _ := rec.CoreFields["approval_status"].(string)
	if status != StatusPending {
		return nil, ErrNotRejectable
	}

	empID, found := s.employeeIDByIdentity(ctx, pool, approverIdentityID)
	canReject := callerIsSuperAdmin
	if found {
		isApprover, err := s.isConfiguredApprover(ctx, pool, id, empID)
		if err != nil {
			return nil, fmt.Errorf("check configured approver: %w", err)
		}
		canReject = canReject || isApprover
	}
	if !canReject {
		return nil, ErrNotApprover
	}

	if _, err := pool.Exec(ctx, `
		UPDATE customer SET
			customer_approval_status = 'rejected', customer_is_approved = FALSE,
			customer_rejected_by = $2, customer_rejected_at = NOW(), customer_rejection_reason = $3,
			customer_updated_at = NOW(),
			customer_record_version = customer_record_version + 1
		WHERE customer_uuid = $1`, id, nullableInt(empID), reason); err != nil {
		return nil, fmt.Errorf("reject customer record: %w", err)
	}
	s.writeHistory(ctx, pool, id, "reject", empID)
	return s.GetRecord(ctx, pool, id)
}

// PendingApprovals lists pending customer records where actorIdentityID is a
// configured active approver who has not yet approved -- the caller's own
// approval queue.
func (s *relationalStore) PendingApprovals(ctx context.Context, pool *pgxpool.Pool, actorIdentityID string) ([]workflow.Record, error) {
	empID, found := s.employeeIDByIdentity(ctx, pool, actorIdentityID)
	if !found {
		return []workflow.Record{}, nil
	}
	rows, err := pool.Query(ctx, recordSelect+`
		WHERE c.customer_deleted_at IS NULL
		  AND c.customer_approval_status = 'pending'
		  AND EXISTS (
			SELECT 1 FROM crm_workflow_approver a
			WHERE a.record_type_id = c.record_type AND a.approver_employee_id = $1
			  AND a.is_active AND a.crm_status_id IS NULL
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM customer_approval ca
			WHERE ca.customer_id = c.customer_id AND ca.approver_employee_id = $1
		  )
		ORDER BY c.customer_created_at DESC`, empID)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	out := []workflow.Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// entryApprovalStatus returns "pending" if recordTypeCode currently has at
// least one active (wildcard, stage-level) approver configured, else
// "approved": a stage nobody is configured to gate can't ever be signed off
// (Approve rejects a non-"pending" record), so leaving it "none" would strand
// the record unapprovable forever. No approver configured means nothing
// gates the stage, so the record auto-approves the moment it enters it.
// Called whenever a record ENTERS a stage -- creation, conversion, or a
// stage-crossing transition -- not on every status change within a stage
// (see checkTransitionGate for the within-stage lock).
func (s *relationalStore) entryApprovalStatus(ctx context.Context, pool *pgxpool.Pool, recordTypeCode string) (string, error) {
	required, err := s.requiredApproversForType(ctx, pool, recordTypeCode)
	if err != nil {
		return "", err
	}
	if required > 0 {
		return StatusPending, nil
	}
	return StatusApproved, nil
}

// requiredApproversForType reports how many distinct active (wildcard)
// approvers are configured for recordTypeCode (e.g. "CUST") -- used when no
// record row exists yet to key off (CreateRecord, ConvertRecord's new
// record). activeApproverCount below is the record-in-hand equivalent.
func (s *relationalStore) requiredApproversForType(ctx context.Context, pool *pgxpool.Pool, recordTypeCode string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT a.approver_employee_id) FROM crm_workflow_approver a
		JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
		WHERE rt.record_type_code = $1 AND a.is_active AND a.crm_status_id IS NULL`,
		recordTypeCode).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count required approvers: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether empID is configured (and active) as a
// stage-level approver for record id's current record type.
func (s *relationalStore) isConfiguredApprover(ctx context.Context, pool *pgxpool.Pool, id string, empID int) (bool, error) {
	var allowed bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM crm_workflow_approver a
			JOIN customer r ON r.customer_uuid = $1
			WHERE a.record_type_id = r.record_type
			  AND a.approver_employee_id = $2 AND a.is_active AND a.crm_status_id IS NULL
		)`, id, empID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("check approver: %w", err)
	}
	return allowed, nil
}

// activeApproverCount reports how many distinct active approvers are
// currently configured for record id's record type -- the number of
// approvals required to finalize it, and the live "is this gated at all"
// signal every gate check uses instead of the stored approval-status column.
func (s *relationalStore) activeApproverCount(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT a.approver_employee_id) FROM crm_workflow_approver a
		JOIN customer r ON r.customer_uuid = $1
		WHERE a.record_type_id = r.record_type AND a.is_active AND a.crm_status_id IS NULL
	`, id).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active approvers: %w", err)
	}
	return count, nil
}

// approvalCount reports how many distinct approvers have already signed off
// on record id this round.
func (s *relationalStore) approvalCount(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM customer_approval ca
		JOIN customer c ON c.customer_id = ca.customer_id
		WHERE c.customer_uuid = $1`, id).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count approvals: %w", err)
	}
	return count, nil
}

// hasApproved reports whether empID has already approved record id this round.
func (s *relationalStore) hasApproved(ctx context.Context, pool *pgxpool.Pool, id string, empID int) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM customer_approval ca
			JOIN customer c ON c.customer_id = ca.customer_id
			WHERE c.customer_uuid = $1 AND ca.approver_employee_id = $2
		)`, id, empID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check already approved: %w", err)
	}
	return exists, nil
}
