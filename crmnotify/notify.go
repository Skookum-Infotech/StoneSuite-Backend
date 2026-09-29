package crmnotify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// approvalStatusPending is customer.customer_approval_status while approvers
// have yet to sign off (crmstore's entryApprovalStatus).
const approvalStatusPending = "pending"

// displayNames label each stage in email copy.
var displayNames = map[string]string{
	KeyLead: "Lead", KeyProspect: "Prospect", KeyCustomer: "Customer",
}

// SendFunc delivers one notification; services.SendNotification in production.
type SendFunc func(context.Context, services.NotificationRequest) error

// NotifyStatus best-effort-emails the recipients the CRM diagram assigns to rec
// entering its current status (create, convert, transition or final approval).
// Statuses the diagram sends nothing for are ignored. Never returns an error:
// the record change has already committed and a Notify outage must not undo it.
func NotifyStatus(ctx context.Context, q workflow.Querier, send SendFunc, actorIdentityID string, rec *workflow.Record) {
	if rec == nil {
		return
	}
	code, _ := rec.CoreFields["crm_status_code"].(string)
	rule, ok := RuleForStatus(rec.WorkflowID, code)
	if !ok {
		return
	}
	deliver(ctx, q, send, actorIdentityID, rec, rule, "")
}

// NotifyCreated is NotifyStatus for a newly minted record (create or convert)
// plus, for a customer awaiting approval, the "Submit for Approval" email to its
// approvers — submission is implicit: a customer is pending the moment it exists.
func NotifyCreated(ctx context.Context, q workflow.Querier, send SendFunc, actorIdentityID string, rec *workflow.Record) {
	NotifyStatus(ctx, q, send, actorIdentityID, rec)
	NotifyApprovalRequestedIfPending(ctx, q, send, actorIdentityID, rec)
}

// NotifyApprovalRequestedIfPending emails a customer's approvers when rec is
// awaiting approval; any other record is ignored. Also used when an edit
// resubmits a rejected customer.
func NotifyApprovalRequestedIfPending(ctx context.Context, q workflow.Querier, send SendFunc, actorIdentityID string, rec *workflow.Record) {
	if rec == nil || rec.WorkflowID != KeyCustomer {
		return
	}
	if status, _ := rec.CoreFields["approval_status"].(string); status != approvalStatusPending {
		return
	}
	deliver(ctx, q, send, actorIdentityID, rec, approvalRequestedRule, "")
}

// NotifyRejected emails the submitter that an approver rejected rec and sent it
// back to Draft, quoting the approver's reason.
func NotifyRejected(ctx context.Context, q workflow.Querier, send SendFunc, actorIdentityID string, rec *workflow.Record, reason string) {
	if rec == nil || rec.WorkflowID != KeyCustomer {
		return
	}
	deliver(ctx, q, send, actorIdentityID, rec, approvalRejectedRule, reason)
}

func deliver(ctx context.Context, q workflow.Querier, send SendFunc, actorIdentityID string, rec *workflow.Record, rule Rule, reason string) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "crmnotify: tenant not resolved, skipping email", "event", rule.EventType, "record_id", rec.ID)
		return
	}
	recipients, err := resolve(ctx, q, rule.Groups, rec)
	if err != nil {
		slog.ErrorContext(ctx, "crmnotify: resolve recipients failed", "event", rule.EventType, "record_id", rec.ID, "error", err)
		return
	}
	if len(recipients) == 0 {
		slog.DebugContext(ctx, "crmnotify: no recipients configured for event", "event", rule.EventType, "record_id", rec.ID)
		return
	}
	if err := send(ctx, BuildRequest(tenant.ID, actorIdentityID, rec, rule, reason, recipients, time.Now())); err != nil {
		slog.ErrorContext(ctx, "crmnotify: send email failed", "event", rule.EventType, "record_id", rec.ID, "error", err)
	}
}

// BuildRequest builds the Notify request (in-app row + email) for one lifecycle
// event on rec, addressed to recipients.
func BuildRequest(tenantID, actorIdentityID string, rec *workflow.Record, rule Rule, reason string, recipients []services.RecipientTarget, at time.Time) services.NotificationRequest {
	kind := displayNames[rec.WorkflowID]
	label := strings.TrimSpace(kind + " " + rec.RecordNumber)
	link := fmt.Sprintf("/crm/%s/%s", rec.WorkflowID, rec.ID)
	n := rule.Note

	details := []services.Detail{{Label: kind + " Number", Value: rec.RecordNumber}}
	if name, _ := rec.CoreFields["customer_name"].(string); name != "" {
		details = append(details, services.Detail{Label: "Name", Value: name})
	}
	if status, _ := rec.CoreFields["crm_status_name"].(string); status != "" {
		details = append(details, services.Detail{Label: "Status", Value: status})
	}
	details = append(details, services.Detail{Label: "Date", Value: services.FormatEmailDate(at)})
	if reason != "" {
		details = append(details, services.Detail{Label: "Reason", Value: reason})
	}
	footer := n.Reason
	if footer == "" {
		footer = "you're a recipient of " + strings.ToLower(kind) + " updates."
	}
	return services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  recipients,
		ActorUserID: actorIdentityID,
		EventType:   rule.EventType,
		Resource:    rec.WorkflowID,
		ResourceID:  rec.ID,
		Title:       label + " " + n.Verb,
		Body:        n.Subtitle,
		Link:        link,
		Channels:    []string{"email"},
		Email: &services.Email{
			Preheader:     label + " " + n.Verb,
			Badge:         n.Badge,
			Icon:          n.Icon,
			Heading:       label,
			HeadingAccent: n.Verb,
			Subtitle:      n.Subtitle,
			Greet:         true,
			Paragraphs:    []services.Paragraph{{services.Text(fmt.Sprintf(n.Lead, label))}},
			Details:       details,
			ActionURL:     services.AppURL(link),
			ActionLabel:   "View " + strings.ToLower(kind),
			Reason:        footer,
		},
	}
}
