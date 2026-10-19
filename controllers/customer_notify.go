// controllers/customer_notify.go
package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// notifyCustomerApproved best-effort-notifies a document's customer, via
// their portal login if they have an active one, that it's been approved.
// No-ops silently (logs and returns) if the customer has no accepted portal
// invite, or their portal access has since been revoked -- this is not an
// error, just nothing to do. A customer flagged Do Not Contact is never
// emailed. Never blocks or fails the caller's HTTP
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
func notifyCustomerApproved(ctx context.Context, cp *tenancy.ControlPlane, actorIdentityID, customerUUID, resource, displayName, number string, amount float64, recordUUID string) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "customer notify: no tenant in context, skipping", "resource", resource, "recordId", recordUUID)
		return
	}
	pool, err := tenancy.PoolFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "customer notify: no tenant pool in context, skipping", "resource", resource, "recordId", recordUUID)
		return
	}
	if customerOptedOut(ctx, crmstore.CustomerDoNotContact, pool, customerUUID, resource, recordUUID) {
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
		services.RecipientTarget{UserID: identity.ID, Email: identity.Email, Name: identity.FullName},
		customerApproval{Resource: resource, DisplayName: displayName, Number: number, Amount: amount, RecordUUID: recordUUID, ApprovedAt: time.Now()}))
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: send notification failed", "resource", resource, "recordId", recordUUID, "error", err)
	}
}

// customerOptedOut reports whether the customer approval email must be skipped
// because the customer is flagged Do Not Contact. A failed lookup also skips
// it (fails closed): emailing a customer who opted out is worse than one
// missed courtesy email.
func customerOptedOut(ctx context.Context, lookup doNotContactLookup, pool *pgxpool.Pool, customerUUID, resource, recordUUID string) bool {
	dnc, err := lookup(ctx, pool, customerUUID)
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: do-not-contact lookup failed, skipping",
			"resource", resource, "recordId", recordUUID, "customer", customerUUID, "error", err)
		return true
	}
	if dnc {
		slog.WarnContext(ctx, "customer notify skipped: customer do not contact",
			"event", "customer_approved_do_not_contact", "resource", resource, "recordId", recordUUID, "customer", customerUUID)
	}
	return dnc
}

// customerApproval is the approved record a customer is told about.
type customerApproval struct {
	Resource, DisplayName, Number, RecordUUID string
	Amount                                    float64
	ApprovedAt                                time.Time
}

// buildCustomerApprovedNotification builds the Notify request telling a
// customer their document was approved; its email content is rendered by
// SendNotification through the shared template.
func buildCustomerApprovedNotification(tenantID, actorIdentityID string, recipient services.RecipientTarget, a customerApproval) services.NotificationRequest {
	const verb = "has been approved"
	link := fmt.Sprintf("/sales/%s/%s", a.Resource, a.RecordUUID)
	subject := fmt.Sprintf("Your %s %s", a.DisplayName, a.Number)
	kind := strings.ToLower(a.DisplayName)
	return services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []services.RecipientTarget{recipient},
		ActorUserID: actorIdentityID,
		EventType:   a.Resource + ".customer_approved",
		Resource:    a.Resource,
		ResourceID:  a.RecordUUID,
		Title:       subject + " " + verb,
		Body:        "Approved.",
		Link:        link,
		Channels:    []string{"email"},
		Email: &services.Email{
			Preheader: subject + " " + verb + ".", Badge: "Approved", Icon: services.IconDocCheck,
			Heading: subject, HeadingAccent: verb + ".",
			Subtitle: "Thank you for your prompt action.",
			Greet:    true,
			Paragraphs: []services.Paragraph{{services.Text(fmt.Sprintf(
				"Your %s %s has been approved. You can view the approved %s in your customer portal anytime.", kind, a.Number, kind))}},
			Details: []services.Detail{
				{Label: a.DisplayName + " Number", Value: a.Number},
				{Label: "Amount", Value: services.FormatEmailMoney("", a.Amount)},
				{Label: "Approved On", Value: services.FormatEmailDate(a.ApprovedAt)},
			},
			ActionURL: services.AppURL(link), ActionLabel: "View " + kind,
		},
	}
}

// approvalFinalized reports whether statusCode is the terminal "approved"
// status that triggers a customer notification.
func approvalFinalized(statusCode string) bool {
	return statusCode == approvalTargetStatus
}
