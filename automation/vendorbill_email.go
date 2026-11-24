package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	vendorBillEventType   = "automation.vendor_bill_due"
	vendorBillTitle       = "Vendor bill due reminder"
	vendorBillActionLabel = "Open in StoneSuite"
	vendorBillOwnerReason    = "you own a vendor bill that is due for payment."
	vendorBillApproverReason = "you are an approver of a vendor bill that is due for payment."
)

// vendorBillTiming words a bill's due date relative to today.
func vendorBillTiming(days int) string {
	switch {
	case days == 0:
		return "due today"
	case days == 1:
		return "due in 1 day"
	case days > 1:
		return fmt.Sprintf("due in %d days", days)
	case days == -1:
		return "1 day overdue"
	}
	return fmt.Sprintf("%d days overdue", -days)
}

// vendorBillBody is the in-app text for n bills.
func vendorBillBody(n int) string {
	if n == 1 {
		return "1 vendor bill needs payment."
	}
	return fmt.Sprintf("%d vendor bills need payment.", n)
}

// vendorBillReasonFor says why this recipient is told: they own every listed bill, or they are an approver.
func vendorBillReasonFor(items []VendorBillDoc, userID string) string {
	for _, it := range items {
		if it.OwnerUserID != userID {
			return vendorBillApproverReason
		}
	}
	return vendorBillOwnerReason
}

// renderVendorBillDigest builds the reminder for one recipient, earliest due date first; reason is the
// "you are receiving this because" clause.
func renderVendorBillDigest(company string, items []VendorBillDoc, today time.Time, reason string) services.Email {
	sorted := append([]VendorBillDoc(nil), items...)
	sort.SliceStable(sorted, func(a, b int) bool {
		if !sameDay(sorted[a].DueDate, sorted[b].DueDate) {
			return sorted[a].DueDate.Before(sorted[b].DueDate)
		}
		return sorted[a].Number < sorted[b].Number
	})
	lead := services.Paragraph{services.Text("This vendor bill is due for payment.")}
	if len(sorted) > 1 {
		lead = services.Paragraph{services.Text("These vendor bills are due for payment.")}
	}
	if company != "" {
		lead = append(lead, services.Text(" Sent by "), services.Bold(company), services.Text("."))
	}
	details := make([]services.Detail, 0, len(sorted))
	for _, it := range sorted {
		details = append(details, services.Detail{
			Label: "Vendor bill " + it.Number,
			Value: fmt.Sprintf("%s, %s owed, %s (%s)", it.VendorName, services.FormatEmailMoney("", it.Balance),
				it.DueDate.Format(arDateLayout), vendorBillTiming(arDaysFromToday(it.DueDate, today))),
		})
	}
	email := services.Email{
		Preheader:  vendorBillTitle,
		Badge:      vendorBillTitle,
		Icon:       services.IconDocClock,
		Heading:    vendorBillTitle,
		Greet:      true,
		Paragraphs: []services.Paragraph{lead},
		Details:    details,
		Reason:     reason,
	}
	if len(sorted) > 0 && sorted[0].Route != "" {
		if email.ActionURL = services.AppURL(sorted[0].Route); email.ActionURL != "" {
			email.ActionLabel = vendorBillActionLabel
		}
	}
	return email
}

// vendorBillRenderer renders vendor-bill digests from live bill data.
type vendorBillRenderer struct{ pool vendorBillDB }

// vendorBillDB is what the renderer needs: a Querier that companyprofile can also read through.
type vendorBillDB interface {
	Querier
	companyprofile.Querier
}

// Render re-checks each bill live, drops ineligible ones with their reason and
// builds one in-app plus email digest addressed to the owner for the rest.
func (r vendorBillRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	var kept []Delivery
	var live []VendorBillDoc
	var recipient vendorBillRecipient
	dropped := map[string]string{}
	for _, it := range g.Items {
		ok, reason, doc, err := vendorBillEligible(ctx, r.pool, it.RecordID, it.Anchor)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		if !ok {
			dropped[it.OccurrenceKey] = reason
			continue
		}
		// Who is responsible can change between planning and sending (the owner is reassigned, an
		// approver is deactivated), so this recipient must still be one of the bill's current ones.
		rcpts, err := vendorBillRecipients(ctx, r.pool, doc)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		me, isRecipient := vendorBillRecipientFor(rcpts, g.RecipientEmail)
		if !isRecipient {
			dropped[it.OccurrenceKey] = reasonRecipientChanged
			continue
		}
		recipient = me
		doc.Route = approvalchain.RecordRoute(resourceVendorBill, doc.UUID)
		kept = append(kept, it)
		live = append(live, doc)
	}
	if len(kept) == 0 {
		return services.NotificationRequest{}, nil, dropped, nil
	}
	profile, err := companyprofile.Get(ctx, r.pool)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("vendor bill digest company profile: %w", err)
	}
	email := renderVendorBillDigest(profile.CompanyName, live, env.Today, vendorBillReasonFor(live, recipient.UserID))
	earliest := live[0]
	for _, d := range live[1:] {
		if d.DueDate.Before(earliest.DueDate) {
			earliest = d
		}
	}
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{UserID: recipient.IdentityID, Email: recipient.Email, Name: strings.TrimSpace(recipient.Name)}},
		EventType:  vendorBillEventType,
		Resource:   resourceVendorBill,
		ResourceID: earliest.UUID,
		Title:      vendorBillTitle,
		Body:       vendorBillBody(len(live)),
		Link:       earliest.Route,
		Email:      &email,
		Channels:   []string{arChannelMail, expiryChannelApp},
	}, kept, dropped, nil
}

// vendorBillRecipientFor finds the recipient with the given email among a bill's current ones.
func vendorBillRecipientFor(rcpts []vendorBillRecipient, email string) (vendorBillRecipient, bool) {
	for _, r := range rcpts {
		if equalFold(r.Email, email) {
			return r, true
		}
	}
	return vendorBillRecipient{}, false
}
