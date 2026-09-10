// controllers/customer_notify.go
package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// notifyCustomerApproved best-effort-notifies a document's customer, via
// their portal login if they have an active one, that it's been approved.
// No-ops silently (logs and returns) if the customer has no accepted portal
// invite, or their portal access has since been revoked -- this is not an
// error, just nothing to do. Never blocks or fails the caller's HTTP
// response: sending this notification is not part of the approve handler's
// contract, matching every Notify* failure semantics elsewhere in this
// codebase (see approvalchain/notify.go).
//
// resource is the workflows.key / route segment (e.g. "invoice") -- it is
// used verbatim both as the notification's Resource/EventType prefix and as
// the /sales/<resource>/<recordUUID> link, since portal and staff sessions
// share the exact same detail routes (see
// docs/superpowers/specs/2026-09-10-customer-portal-approval-notifications-design.md
// §2).
func notifyCustomerApproved(ctx context.Context, cp *tenancy.ControlPlane, actorIdentityID, customerUUID, resource, displayName, number, recordUUID string) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "customer notify: no tenant in context, skipping", "resource", resource, "recordId", recordUUID)
		return
	}
	invite, err := cp.PortalInviteForCustomer(ctx, tenant.ID, customerUUID)
	if errors.Is(err, tenancy.ErrPortalInviteNotFound) {
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: resolve portal invite failed", "resource", resource, "recordId", recordUUID, "error", err)
		return
	}
	active, err := cp.PortalLinkActive(ctx, invite.IdentityID, tenant.ID)
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: check portal link active failed", "resource", resource, "recordId", recordUUID, "error", err)
		return
	}
	if !active {
		return
	}
	identity, err := cp.IdentityByID(ctx, invite.IdentityID)
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: resolve identity failed", "resource", resource, "recordId", recordUUID, "error", err)
		return
	}
	err = services.SendNotification(ctx, services.NotificationRequest{
		TenantID:    tenant.ID,
		Recipients:  []services.RecipientTarget{{UserID: identity.ID, Email: identity.Email}},
		ActorUserID: actorIdentityID,
		EventType:   resource + ".customer_approved",
		Resource:    resource,
		ResourceID:  recordUUID,
		Title:       fmt.Sprintf("Your %s %s has been approved", displayName, number),
		Body:        "Approved.",
		Link:        fmt.Sprintf("/sales/%s/%s", resource, recordUUID),
		Channels:    []string{"email"},
	})
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: send notification failed", "resource", resource, "recordId", recordUUID, "error", err)
	}
}
