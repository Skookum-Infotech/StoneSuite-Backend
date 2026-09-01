// quote/notify.go
package quote

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

const (
	quoteApproverTable = "quote_approver"
	quoteApprovalTable = "quote_approval"
	quoteTable         = "quote"
	quoteIDColumn      = "quote_id"
	quoteNumberColumn  = "quote_number"
	quoteOwnerColumn   = "quote_owner_id"
	quoteResource      = "quote"
	quoteDisplayName   = "Quote"
)

// notifyTransition best-effort-notifies approvers/owner around a manual
// status move (AD-8): approvers when the quote just entered a gated status,
// or the owner when a gated-unapproved quote escaped via an always-allowed
// exit (void/cancel/reject) instead of clearing approval. Never fails the
// caller's transition -- see approvalchain.NotifyApprovalRequested's own
// failure contract.
func notifyTransition(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, toStatusID int, toStatusCode string, requiredHere int, approvalStatus string, targetApprovers int, actorEmployeeID int) {
	if targetApprovers > 0 {
		approvalchain.NotifyApprovalRequested(ctx, pool, approvalchain.EventContext{
			Table: quoteTable, IDColumn: quoteIDColumn, NumberColumn: quoteNumberColumn,
			ApproverTable: quoteApproverTable, RecordTypeID: recordTypeID, StatusID: toStatusID,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: quoteResource, DisplayName: quoteDisplayName, RecordUUID: uuid,
		})
	}
	if requiredHere > 0 && approvalStatus != approvalApproved && approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		approvalchain.NotifyApprovalRejected(ctx, pool, approvalchain.EventContext{
			Table: quoteTable, IDColumn: quoteIDColumn, NumberColumn: quoteNumberColumn, OwnerColumn: quoteOwnerColumn,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: quoteResource, DisplayName: quoteDisplayName, RecordUUID: uuid,
		})
	}
}

// notifyCreated best-effort-notifies the actor who created a new quote that
// the creation succeeded.
func notifyCreated(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, actorEmployeeID int) {
	approvalchain.NotifyCreated(ctx, pool, approvalchain.EventContext{
		Table: quoteTable, IDColumn: quoteIDColumn, NumberColumn: quoteNumberColumn,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: quoteResource, DisplayName: quoteDisplayName, RecordUUID: uuid,
	})
}

// notifyApproved best-effort-notifies a quote's owner once it's approved
// (quorum met or a super-admin override).
func notifyApproved(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, approverEmployeeID int) {
	approvalchain.NotifyApproved(ctx, pool, approvalchain.EventContext{
		Table: quoteTable, IDColumn: quoteIDColumn, NumberColumn: quoteNumberColumn, OwnerColumn: quoteOwnerColumn,
		InternalID: internalID, ActorEmployeeID: approverEmployeeID,
		Resource: quoteResource, DisplayName: quoteDisplayName, RecordUUID: uuid,
	})
}

// notifyRemainingApprovers best-effort-notifies the approvers who haven't
// yet signed off on a quote after one more sign-off was just recorded but
// quorum still isn't met.
func notifyRemainingApprovers(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, statusID, actorEmployeeID int) {
	approvalchain.NotifyRemainingApprovers(ctx, pool, approvalchain.EventContext{
		Table: quoteTable, IDColumn: quoteIDColumn, NumberColumn: quoteNumberColumn,
		ApproverTable: quoteApproverTable, ApprovalTable: quoteApprovalTable,
		RecordTypeID: recordTypeID, StatusID: statusID, InternalID: internalID,
		ActorEmployeeID: actorEmployeeID, Resource: quoteResource, DisplayName: quoteDisplayName, RecordUUID: uuid,
	})
}
