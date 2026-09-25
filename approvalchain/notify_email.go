// approvalchain/notify_email.go
package approvalchain

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

// approvalNote is the wording of one approval-chain notification, shared by
// the in-app row (title, body) and its email. Lead is the email's opening
// sentence with %s standing for the record label ("Invoice INV-000123").
// The notes below are read-only.
type approvalNote struct {
	Badge     string // banner pill, e.g. "Approval Needed"
	Verb      string // what happened: title tail and banner heading line 2
	Body      string // in-app notification body
	CTAVerb   string // button verb, joined with the module name: "Review" -> "Review invoice"
	Icon      services.EmailIcon
	Subtitle  string // banner sub-line
	Lead      string // email opening sentence, see above
	DateLabel string // details-box date row label; empty omits the row
	Reason    string // footer "You're receiving this because ..."
}

var (
	noteApprovalRequested = approvalNote{
		Badge: "Approval Needed", Verb: "needs your approval", Body: "Submitted for approval.", CTAVerb: "Review",
		Icon: services.IconDocClock, Subtitle: "Please review and take action.",
		Lead:      "%s has been submitted for your approval. Review the details below, then approve or reject it.",
		DateLabel: "Submitted On", Reason: "you're an approver.",
	}
	noteApprovalReminder = approvalNote{
		Badge: "Approval Reminder", Verb: "still needs your approval", Body: "Still awaiting your sign-off.", CTAVerb: "Review",
		Icon: services.IconDocClock, Subtitle: "Your sign-off is still pending.",
		Lead:   "%s is still waiting for your approval. Review the details below, then approve or reject it.",
		Reason: "you're an approver.",
	}
	noteApproved = approvalNote{
		Badge: "Approved", Verb: "was approved", Body: "Approved.", CTAVerb: "View",
		Icon: services.IconDocCheck, Subtitle: "All required approvals are complete.",
		Lead:      "%s has been approved. You can open the record anytime to view it.",
		DateLabel: "Approved On", Reason: "you're the record owner.",
	}
	noteSentBack = approvalNote{
		Badge: "Sent Back", Verb: "was sent back", Body: "Sent back for changes.", CTAVerb: "View",
		Icon: services.IconDocArrow, Subtitle: "Please review and resubmit.",
		Lead:      "%s was sent back for changes. Open the record to review it and resubmit.",
		DateLabel: "Sent Back On", Reason: "you're the record owner.",
	}
	noteCreated = approvalNote{
		Badge: "Created", Verb: "was created", Body: "Created.", CTAVerb: "View",
		Icon: services.IconDocPlus, Subtitle: "Your record is ready.",
		Lead:      "%s was created successfully. You can open the record anytime to continue working on it.",
		DateLabel: "Created On", Reason: "you created this record.",
	}
)

// recordFacts is what the email's details box shows about the record.
type recordFacts struct {
	Number     string
	Amount     string // formatted; empty when the module has no amount column
	OccurredAt time.Time
}

// buildApprovalNotification builds the Notify request for one approval-chain
// event. The email content is filled in here and rendered by SendNotification
// through the shared template, greeting each recipient by name.
func buildApprovalNotification(tenantID, eventType, actorUserID string, facts recordFacts, ec EventContext, note approvalNote, contacts []contact) services.NotificationRequest {
	subject := fmt.Sprintf("%s %s", ec.DisplayName, facts.Number)
	link := resourceRoute(ec.Resource, ec.RecordUUID)
	details := []services.Detail{{Label: ec.DisplayName + " Number", Value: facts.Number}}
	if facts.Amount != "" {
		details = append(details, services.Detail{Label: "Amount", Value: facts.Amount})
	}
	if note.DateLabel != "" {
		details = append(details, services.Detail{Label: note.DateLabel, Value: services.FormatEmailDate(facts.OccurredAt)})
	}
	// The approver's reason for sending a record back (EventContext.Detail, set
	// only by NotifyApprovalRejected) is free text; the template escapes it.
	if ec.Detail != "" {
		details = append(details, services.Detail{Label: "Reason", Value: ec.Detail})
	}
	return services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  contactsToRecipients(contacts),
		ActorUserID: actorUserID,
		EventType:   eventType,
		Resource:    ec.Resource,
		ResourceID:  ec.RecordUUID,
		Title:       subject + " " + note.Verb,
		Body:        note.Body,
		Link:        link,
		Channels:    []string{"email"},
		Email: &services.Email{
			Preheader: subject + " " + note.Verb + ".",
			Badge:     note.Badge, Icon: note.Icon,
			Heading: subject, HeadingAccent: note.Verb + ".",
			Subtitle:    note.Subtitle,
			Greet:       true,
			Paragraphs:  []services.Paragraph{{services.Text(fmt.Sprintf(note.Lead, subject))}},
			Details:     details,
			ActionURL:   services.AppURL(link),
			ActionLabel: note.CTAVerb + " " + strings.ToLower(ec.DisplayName),
			Reason:      note.Reason,
		},
	}
}

func sendApprovalNotification(ctx context.Context, tenantID, eventType string, facts recordFacts, actorUserID string, ec EventContext, note approvalNote, contacts []contact) {
	err := services.SendNotification(ctx, buildApprovalNotification(tenantID, eventType, actorUserID, facts, ec, note, contacts))
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: send approval notification failed", "eventType", eventType, "resourceId", ec.RecordUUID, "error", err)
	}
}

// amountColumns maps a record table to its total-amount column, shown as the
// "Amount" row of approval emails. Tables without one (requisition,
// fabrication_job) simply omit the row.
var amountColumns = map[string]string{
	"estimate": "estimate_grand_total", "quote": "quote_grand_total", "sales_order": "sales_order_grand_total",
	"purchase_order": "purchase_order_grand_total", "vendor_bill": "vendor_bill_grand_total",
	"vendor_payment": "vendor_payment_amount", "expense": "expense_total", "invoice": "invoice_grand_total",
	"payment": "payment_amount", "credit_memo": "credit_memo_grand_total", "refund": "refund_amount",
	"vendor_credit": "vendor_credit_grand_total",
}

// fetchFacts reads the record number (required) and, best-effort, its amount
// for the email's details box.
func fetchFacts(ctx context.Context, q workflow.Querier, ec EventContext) (recordFacts, error) {
	number, err := fetchNumber(ctx, q, ec.Table, ec.IDColumn, ec.NumberColumn, ec.InternalID)
	if err != nil {
		return recordFacts{}, err
	}
	facts := recordFacts{Number: number, OccurredAt: time.Now()}
	col, ok := amountColumns[ec.Table]
	if !ok {
		return facts, nil
	}
	var amount float64
	query := fmt.Sprintf(`SELECT %s::float8 FROM %s WHERE %s = $1`, col, ec.Table, ec.IDColumn)
	if err := q.QueryRow(ctx, query, ec.InternalID).Scan(&amount); err != nil {
		slog.WarnContext(ctx, "approvalchain: resolve record amount for notification failed", "table", ec.Table, "error", err)
		return facts, nil
	}
	facts.Amount = services.FormatEmailMoney("", amount)
	return facts, nil
}
