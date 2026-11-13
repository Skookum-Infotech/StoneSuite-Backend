// controllers/customer_status_notify.go
package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// Event-type suffixes appended to "<resource>." for customer emails.
const (
	customerEventApproved  = "customer_approved"
	customerEventStatus    = "customer_status"
	customerEventDeleted   = "customer_deleted"
	customerEventReceived  = "customer_received"
	customerEventSubmitted = "customer_submitted"
)

// Resource keys the customer status copy table is keyed by.
const (
	custResSalesOrder     = "sales_order"
	custResQuote          = "quote"
	custResInvoice        = "invoice"
	custResPayment        = "payment"
	custResRefund         = "refund"
	custResCreditMemo     = "credit_memo"
	custResFabricationJob = "fabrication_job"
	// Display names and status codes the money-document handlers pass in.
	custDisplayInvoice      = "Invoice"
	custDisplayPayment      = "Payment"
	custDisplayRefund       = "Refund"
	custDisplayCreditMemo   = "Credit Memo"
	invoiceStatusPart       = "PART"
	invoiceStatusPaid       = "PAID"
	creditMemoStatusApplied = "APPL"
	// custRouteInstallation is the frontend route segment fabrication jobs use.
	custRouteInstallation = "installation"
)

const (
	custLinkFormat = "/sales/%s/%s"
	// custLinkCreatedOrders is the customer portal's Created Orders page.
	custLinkCreatedOrders = "/sales/created-orders"
	custChannelEmail      = "email"
	custDefaultDateText   = "Updated On"
	custAmountLabel       = "Amount"
	custNumberSuffix      = " Number"
	custViewPrefix        = "View "
	custApprovedSubtle    = "Thank you for your prompt action."
	custApprovedBadge     = "Approved"
	custApprovedVerb      = "has been approved"
	custApprovedDate      = "Approved On"
)

// customerEvent is a record event a customer is told about by email.
type customerEvent struct {
	Resource, DisplayName, Number, RecordUUID string
	// EventSuffix is appended to "<resource>." to form the notification EventType.
	EventSuffix string
	Badge       string
	// Heading is the banner subject; HeadingAccent the highlighted verb phrase.
	Heading, HeadingAccent string
	Subtitle               string
	// Body is the email paragraph text.
	Body string
	// ActionLabel is the button label; defaults to "View <display name>".
	ActionLabel string
	// DateLabel labels the OccurredAt detail row.
	DateLabel  string
	Amount     float64
	OccurredAt time.Time
	Icon       services.EmailIcon
	// LinkOverride, when set, replaces the default record route for the email link.
	LinkOverride string
}

// statusCopy is the customer-facing wording for one event.
type statusCopy struct {
	Badge    string
	Verb     string
	Subtitle string
	// Body is a format with two %s verbs: lower-cased document kind, then number.
	Body      string
	DateLabel string
	Icon      services.EmailIcon
}

// event builds a customerEvent for rec from this copy under eventSuffix.
func (c statusCopy) event(eventSuffix, resource, displayName, number, recordUUID string, amount float64, at time.Time) customerEvent {
	kind := strings.ToLower(displayName)
	return customerEvent{
		Resource: resource, DisplayName: displayName, Number: number, RecordUUID: recordUUID,
		EventSuffix: eventSuffix, Badge: c.Badge,
		Heading:       fmt.Sprintf("Your %s %s", displayName, number),
		HeadingAccent: c.Verb + ".",
		Subtitle:      c.Subtitle,
		Body:          fmt.Sprintf(c.Body, kind, number),
		DateLabel:     c.DateLabel, Amount: amount, OccurredAt: at, Icon: c.Icon,
	}
}

// customerStatusEvent builds the status-change event, or false when the
// resource/status pair is not one customers are emailed about.
func customerStatusEvent(resource, statusCode, displayName, number, recordUUID string, amount float64, at time.Time) (customerEvent, bool) {
	c, ok := customerStatusCopy(resource, statusCode)
	if !ok {
		return customerEvent{}, false
	}
	return c.event(customerEventStatus, resource, displayName, number, recordUUID, amount, at), true
}

// notifyCustomerStatus best-effort-notifies the customer of a status change
// when the resource/status pair is one customers are emailed about.
func notifyCustomerStatus(ctx context.Context, cp *tenancy.ControlPlane, actorIdentityID, customerUUID, resource, statusCode, displayName, number string, amount float64, recordUUID string) {
	ev, ok := customerStatusEvent(resource, statusCode, displayName, number, recordUUID, amount, time.Now())
	if !ok {
		return
	}
	notifyCustomerEvent(ctx, cp, actorIdentityID, customerUUID, ev)
}

// notifyCustomerEvent best-effort-notifies a customer, via their active portal
// login, of ev. It no-ops silently when the customer has no accepted or active
// portal login, never emails a Do Not Contact customer, and never fails the
// caller: failures are logged only.
func notifyCustomerEvent(ctx context.Context, cp *tenancy.ControlPlane, actorIdentityID, customerUUID string, ev customerEvent) {
	resource, recordUUID := ev.Resource, ev.RecordUUID
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
	err = services.SendNotification(ctx, buildCustomerEventNotification(tenant.ID, actorIdentityID,
		services.RecipientTarget{UserID: identity.ID, Email: identity.Email, Name: identity.FullName}, ev))
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: send notification failed", "resource", resource, "recordId", recordUUID, "error", fmt.Errorf("send %s%s: %w", resource, ev.EventSuffix, err))
	}
}

// notifyCustomerEventTo best-effort-notifies an already-known recipient (the
// portal user acting in this request) of ev. Unlike notifyCustomerEvent it does
// no portal-invite lookup, but still never emails a Do Not Contact customer,
// fails closed, and never fails the caller: failures are logged only.
func notifyCustomerEventTo(ctx context.Context, actorIdentityID, customerUUID string, recipient services.RecipientTarget, ev customerEvent) {
	resource, recordUUID := ev.Resource, ev.RecordUUID
	if recipient.Email == "" {
		slog.WarnContext(ctx, "customer notify: recipient has no email, skipping", "resource", resource, "recordId", recordUUID)
		return
	}
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
	err = services.SendNotification(ctx, buildCustomerEventNotification(tenant.ID, actorIdentityID, recipient, ev))
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: send notification failed", "resource", resource, "recordId", recordUUID, "error", fmt.Errorf("send %s%s: %w", resource, ev.EventSuffix, err))
	}
}

// customerEventLink is the portal/staff detail route for ev's record.
func customerEventLink(resource, recordUUID string) string {
	if resource == custResFabricationJob {
		return approvalchain.RecordRoute(custRouteInstallation, recordUUID)
	}
	return fmt.Sprintf(custLinkFormat, resource, recordUUID)
}

// buildCustomerEventNotification builds the Notify request telling a customer
// about ev; its email content is rendered by SendNotification through the
// shared template.
func buildCustomerEventNotification(tenantID, actorIdentityID string, recipient services.RecipientTarget, ev customerEvent) services.NotificationRequest {
	link := customerEventLink(ev.Resource, ev.RecordUUID)
	if ev.LinkOverride != "" {
		link = ev.LinkOverride
	}
	verb := strings.TrimSuffix(ev.HeadingAccent, ".")
	title := ev.Heading + " " + verb
	kind := strings.ToLower(ev.DisplayName)
	actionLabel := ev.ActionLabel
	if actionLabel == "" {
		actionLabel = custViewPrefix + kind
	}
	dateLabel := ev.DateLabel
	if dateLabel == "" {
		dateLabel = custDefaultDateText
	}
	details := []services.Detail{{Label: ev.DisplayName + custNumberSuffix, Value: ev.Number}}
	if ev.Amount != 0 {
		details = append(details, services.Detail{Label: custAmountLabel, Value: services.FormatEmailMoney("", ev.Amount)})
	}
	details = append(details, services.Detail{Label: dateLabel, Value: services.FormatEmailDate(ev.OccurredAt)})
	return services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []services.RecipientTarget{recipient},
		ActorUserID: actorIdentityID,
		EventType:   ev.Resource + "." + ev.EventSuffix,
		Resource:    ev.Resource,
		ResourceID:  ev.RecordUUID,
		Title:       title,
		Body:        ev.Badge + ".",
		Link:        link,
		Channels:    []string{custChannelEmail},
		Email: &services.Email{
			Preheader: title + ".", Badge: ev.Badge, Icon: ev.Icon,
			Heading: ev.Heading, HeadingAccent: ev.HeadingAccent,
			Subtitle:   ev.Subtitle,
			Greet:      true,
			Paragraphs: []services.Paragraph{{services.Text(ev.Body)}},
			Details:    details,
			ActionURL:  services.AppURL(link), ActionLabel: actionLabel,
		},
	}
}

// customerStatusCopyTable is the customer-facing wording per resource and
// status code. A pair absent from it is never emailed.
var customerStatusCopyTable = map[string]map[string]statusCopy{
	custResSalesOrder: {
		"OPEN": {"Confirmed", "has been confirmed", "We have received your order.", "Your %s %s has been confirmed and is now open.", "Confirmed On", services.IconDocCheck},
		"PART": {"Partially Fulfilled", "has been partially fulfilled", "Part of your order is on its way.", "Part of your %s %s has been fulfilled. The remainder will follow.", "Updated On", services.IconDocArrow},
		"FILL": {"Fulfilled", "has been fulfilled", "Your order is complete.", "Your %s %s has been fully fulfilled.", "Fulfilled On", services.IconDocCheck},
		"CANC": {"Cancelled", "has been cancelled", "This order will not proceed.", "Your %s %s has been cancelled. Please contact us if you have any questions.", "Cancelled On", services.IconDocClock},
	},
	custResQuote: {
		"SENT": {"Ready for Review", "is ready for your review", "Please take a look when you can.", "Your %s %s is ready for your review. You can view it in your customer portal.", "Sent On", services.IconDocArrow},
	},
	custResInvoice: {
		"SENT": {"Invoice Issued", "is ready", "A new invoice is available.", "Your %s %s is ready. You can view and review it in your customer portal.", "Issued On", services.IconDocArrow},
		"PART": {"Partially Paid", "has been partially paid", "We have applied your payment.", "A payment has been applied to your %s %s. A balance remains.", "Updated On", services.IconDocCheck},
		"PAID": {"Paid", "has been paid in full", "Thank you for your payment.", "Your %s %s has been paid in full. Thank you.", "Paid On", services.IconDocCheck},
		"VOID": {"Voided", "has been voided", "No payment is required.", "Your %s %s has been voided and no payment is required.", "Voided On", services.IconDocClock},
		"ODUE": {"Overdue", "is overdue", "Please arrange payment.", "Your %s %s is now past its due date. Please arrange payment at your earliest convenience.", "Updated On", services.IconDocClock},
	},
	custResPayment: {
		"DEPO": {"Payment Received", "has been received", "Thank you for your payment.", "We have received your %s %s. Thank you.", "Received On", services.IconDocCheck},
	},
	custResRefund: {
		"SENT": {"Refund Issued", "has been issued", "Your refund is on its way.", "Your %s %s has been issued.", "Issued On", services.IconDocArrow},
	},
	custResCreditMemo: {
		"APPV": {"Credit Approved", "has been approved", "A credit has been added to your account.", "Your %s %s has been approved.", "Approved On", services.IconDocCheck},
		"APPL": {"Credit Applied", "has been applied", "Your credit has been applied.", "Your %s %s has been applied to your account.", "Applied On", services.IconDocCheck},
	},
	custResFabricationJob: {
		"ORCV": {"Order Received", "has been received", "We have your job on file.", "We have received your %s %s and will begin preparing it.", "Updated On", services.IconDocPlus},
		"MALC": {"Materials Allocated", "has materials allocated", "Materials are set aside for your job.", "Materials have been allocated for your %s %s.", "Updated On", services.IconDocCheck},
		"TMPL": {"Templating", "is being templated", "We are measuring and templating.", "Your %s %s is now in templating.", "Updated On", services.IconDocClock},
		"TAPV": {"Template Approved", "has an approved template", "Your template is approved.", "The template for your %s %s has been approved.", "Updated On", services.IconDocCheck},
		"FRDY": {"Ready for Fabrication", "is ready for fabrication", "Fabrication is next.", "Your %s %s is ready for fabrication.", "Updated On", services.IconDocCheck},
		"CUTG": {"Cutting", "is being cut", "Work on your pieces has begun.", "Your %s %s is now being cut.", "Updated On", services.IconDocClock},
		"EDGP": {"Edge Profiling", "is in edge profiling", "We are finishing your edges.", "Your %s %s is now in edge profiling.", "Updated On", services.IconDocClock},
		"QCPD": {"Quality Check", "is in quality check", "We are inspecting your pieces.", "Your %s %s is now in quality check.", "Updated On", services.IconDocClock},
		"QCPS": {"Quality Check Passed", "has passed quality check", "Your pieces passed inspection.", "Your %s %s has passed quality check.", "Updated On", services.IconDocCheck},
		"RSHP": {"Ready to Ship", "is ready to ship", "Your order is ready.", "Your %s %s is ready to ship.", "Updated On", services.IconDocArrow},
		"TRAN": {"In Transit", "is in transit", "Your order is on its way.", "Your %s %s is on its way to you.", "Updated On", services.IconPlane},
		"INST": {"Installation", "is being installed", "Installation is underway.", "Installation of your %s %s is underway.", "Updated On", services.IconDocClock},
		"COMP": {"Completed", "is complete", "Thank you for your business.", "Your %s %s is complete. Thank you for choosing us.", "Completed On", services.IconDocCheck},
		"HOLD": {"On Hold", "is on hold", "We will update you soon.", "Your %s %s is temporarily on hold. We will be in touch with next steps.", "Updated On", services.IconDocClock},
		"CANC": {"Cancelled", "has been cancelled", "This job will not proceed.", "Your %s %s has been cancelled. Please contact us if you have any questions.", "Cancelled On", services.IconDocClock},
	},
}

// customerStatusCopy returns the customer-facing copy for a resource's status
// code, or false when customers are not emailed about that pair.
func customerStatusCopy(resource, statusCode string) (statusCopy, bool) {
	c, ok := customerStatusCopyTable[resource][statusCode]
	return c, ok
}

// customerReceivedCopy is the copy for a payment being applied to a document.
func customerReceivedCopy() statusCopy {
	return statusCopy{"Payment Received", "has received a payment", "Thank you for your payment.", "We have received a payment for your %s %s. Thank you.", "Received On", services.IconDocCheck}
}

// customerDeletedCopy is the copy for a document being removed.
func customerDeletedCopy() statusCopy {
	return statusCopy{"Removed", "has been removed", "No further action is needed.", "Your %s %s has been removed. Please contact us if you have any questions.", "Removed On", services.IconDocClock}
}

// customerSubmittedCopy is the copy confirming a customer's portal submission.
func customerSubmittedCopy() statusCopy {
	return statusCopy{"Submitted", "has been submitted", "Thank you. We have your submission.", "We have received your %s %s. We will be in touch shortly.", "Submitted On", services.IconDocCheck}
}
