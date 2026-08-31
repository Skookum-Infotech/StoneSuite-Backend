// salesorder/notify.go
package salesorder

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

const (
	salesOrderApproverTable = "sales_order_approver"
	salesOrderApprovalTable = "sales_order_approval"
	salesOrderTable         = "sales_order"
	salesOrderIDColumn      = "sales_order_id"
	salesOrderNumberColumn  = "sales_order_number"
	salesOrderOwnerColumn   = "sales_order_owner_id"
	salesOrderResource      = "sales_order"
	salesOrderDisplayName   = "Sales Order"
)

// notifyTransition best-effort-notifies approvers/owner around a manual
// status move (AD-10): approvers when the order just entered a gated
// status, or the owner when a gated-unapproved order escaped via an
// always-allowed exit (void/cancel/reject) instead of clearing approval.
// Never fails the caller's transition -- see
// approvalchain.NotifyApprovalRequested's own failure contract.
func notifyTransition(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, toStatusID int, toStatusCode string, requiredHere int, approvalStatus string, targetApprovers int, actorEmployeeID int) {
	if targetApprovers > 0 {
		approvalchain.NotifyApprovalRequested(ctx, pool, approvalchain.EventContext{
			Table: salesOrderTable, IDColumn: salesOrderIDColumn, NumberColumn: salesOrderNumberColumn,
			ApproverTable: salesOrderApproverTable, RecordTypeID: recordTypeID, StatusID: toStatusID,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: salesOrderResource, DisplayName: salesOrderDisplayName, RecordUUID: uuid,
		})
	}
	if requiredHere > 0 && approvalStatus != approvalApproved && approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		approvalchain.NotifyApprovalRejected(ctx, pool, approvalchain.EventContext{
			Table: salesOrderTable, IDColumn: salesOrderIDColumn, NumberColumn: salesOrderNumberColumn, OwnerColumn: salesOrderOwnerColumn,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: salesOrderResource, DisplayName: salesOrderDisplayName, RecordUUID: uuid,
		})
	}
}

// notifyApproved best-effort-notifies an order's owner once it's approved
// (quorum met or a super-admin override).
func notifyApproved(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, approverEmployeeID int) {
	approvalchain.NotifyApproved(ctx, pool, approvalchain.EventContext{
		Table: salesOrderTable, IDColumn: salesOrderIDColumn, NumberColumn: salesOrderNumberColumn, OwnerColumn: salesOrderOwnerColumn,
		InternalID: internalID, ActorEmployeeID: approverEmployeeID,
		Resource: salesOrderResource, DisplayName: salesOrderDisplayName, RecordUUID: uuid,
	})
}

// notifyRemainingApprovers best-effort-notifies the approvers who haven't
// yet signed off on an order after one more sign-off was just recorded but
// quorum still isn't met.
func notifyRemainingApprovers(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, statusID, actorEmployeeID int) {
	approvalchain.NotifyRemainingApprovers(ctx, pool, approvalchain.EventContext{
		Table: salesOrderTable, IDColumn: salesOrderIDColumn, NumberColumn: salesOrderNumberColumn,
		ApproverTable: salesOrderApproverTable, ApprovalTable: salesOrderApprovalTable,
		RecordTypeID: recordTypeID, StatusID: statusID, InternalID: internalID,
		ActorEmployeeID: actorEmployeeID, Resource: salesOrderResource, DisplayName: salesOrderDisplayName, RecordUUID: uuid,
	})
}
