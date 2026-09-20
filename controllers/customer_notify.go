// controllers/customer_notify.go
package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
	err = services.SendNotification(ctx, buildCustomerApprovedNotification(tenant.ID, actorIdentityID,
		services.RecipientTarget{UserID: identity.ID, Email: identity.Email}, resource, displayName, number, recordUUID))
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: send notification failed", "resource", resource, "recordId", recordUUID, "error", err)
	}
}

// buildCustomerApprovedNotification builds the Notify request telling a
// customer their document was approved. The email body is built here, through
// the shared StoneSuite shell, rather than left to stonesuite-notify's bare
// generic template — which is unescaped and only knows the relative in-app route.
func buildCustomerApprovedNotification(tenantID, actorIdentityID string, recipient services.RecipientTarget, resource, displayName, number, recordUUID string) services.NotificationRequest {
	const verb = "has been approved"
	link := fmt.Sprintf("/sales/%s/%s", resource, recordUUID)
	subject := fmt.Sprintf("Your %s %s", displayName, number)
	kind := strings.ToLower(displayName)
	return services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []services.RecipientTarget{recipient},
		ActorUserID: actorIdentityID,
		EventType:   resource + ".customer_approved",
		Resource:    resource,
		ResourceID:  recordUUID,
		Title:       subject + " " + verb,
		Body:        "Approved.",
		Link:        link,
		Channels:    []string{"email"},
		EmailBodyHTML: services.BuildRecordEmailHTML(services.RecordEmail{
			Badge:   "Approved",
			Subject: subject,
			Verb:    verb,
			Message: fmt.Sprintf("Your %s %s has been approved. You can view it any time in your customer portal.", kind, number),
			Path:    link,
			CTA:     "View " + kind,
		}),
	}
}

// approvalFinalized reports whether statusCode is the terminal "approved"
// status that triggers a customer notification.
func approvalFinalized(statusCode string) bool {
	return statusCode == approvalTargetStatus
}
