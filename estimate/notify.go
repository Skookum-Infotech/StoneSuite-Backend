// estimate/notify.go
package estimate

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

const (
	estimateApproverTable = "estimate_approver"
	estimateApprovalTable = "estimate_approval"
	estimateTable         = "estimate"
	estimateIDColumn      = "estimate_id"
	estimateNumberColumn  = "estimate_number"
	estimateOwnerColumn   = "estimate_owner_id"
	estimateResource      = "estimate"
	estimateDisplayName   = "Estimate"
)

// notifyTransition best-effort-notifies approvers/owner around a manual
// status move (AD-8): approvers when the estimate just entered a gated
// status, or the owner when a gated-unapproved estimate escaped via an
// always-allowed exit (void/cancel/reject) instead of clearing approval.
// Never fails the caller's transition -- see
// approvalchain.NotifyApprovalRequested's own failure contract.
func notifyTransition(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, toStatusID int, toStatusCode string, requiredHere int, approvalStatus string, targetApprovers int, actorEmployeeID int) {
	if targetApprovers > 0 {
		approvalchain.NotifyApprovalRequested(ctx, pool, approvalchain.EventContext{
			Table: estimateTable, IDColumn: estimateIDColumn, NumberColumn: estimateNumberColumn,
			ApproverTable: estimateApproverTable, RecordTypeID: recordTypeID, StatusID: toStatusID,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: estimateResource, DisplayName: estimateDisplayName, RecordUUID: uuid,
		})
	}
	if requiredHere > 0 && approvalStatus != approvalApproved && approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		approvalchain.NotifyApprovalRejected(ctx, pool, approvalchain.EventContext{
			Table: estimateTable, IDColumn: estimateIDColumn, NumberColumn: estimateNumberColumn, OwnerColumn: estimateOwnerColumn,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: estimateResource, DisplayName: estimateDisplayName, RecordUUID: uuid,
		})
	}
}

// notifyCreated best-effort-notifies the actor who created a new estimate
// that the creation succeeded.
func notifyCreated(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, actorEmployeeID int) {
	approvalchain.NotifyCreated(ctx, pool, approvalchain.EventContext{
		Table: estimateTable, IDColumn: estimateIDColumn, NumberColumn: estimateNumberColumn,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: estimateResource, DisplayName: estimateDisplayName, RecordUUID: uuid,
	})
}

// notifyApproved best-effort-notifies an estimate's owner once it's
// approved (quorum met or a super-admin override).
func notifyApproved(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, approverEmployeeID int) {
	approvalchain.NotifyApproved(ctx, pool, approvalchain.EventContext{
		Table: estimateTable, IDColumn: estimateIDColumn, NumberColumn: estimateNumberColumn, OwnerColumn: estimateOwnerColumn,
		InternalID: internalID, ActorEmployeeID: approverEmployeeID,
		Resource: estimateResource, DisplayName: estimateDisplayName, RecordUUID: uuid,
	})
}

// notifyRemainingApprovers best-effort-notifies the approvers who haven't
// yet signed off on an estimate after one more sign-off was just recorded
// but quorum still isn't met.
func notifyRemainingApprovers(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, statusID, actorEmployeeID int) {
	approvalchain.NotifyRemainingApprovers(ctx, pool, approvalchain.EventContext{
		Table: estimateTable, IDColumn: estimateIDColumn, NumberColumn: estimateNumberColumn,
		ApproverTable: estimateApproverTable, ApprovalTable: estimateApprovalTable,
		RecordTypeID: recordTypeID, StatusID: statusID, InternalID: internalID,
		ActorEmployeeID: actorEmployeeID, Resource: estimateResource, DisplayName: estimateDisplayName, RecordUUID: uuid,
	})
}
