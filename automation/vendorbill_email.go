package automation

import (
	"context"
	"fmt"
	"sort"
	"time"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	vendorBillEventType   = "automation.vendor_bill_due"
	vendorBillTitle       = "Vendor bill due reminder"
	vendorBillActionLabel = "Open in StoneSuite"
	vendorBillEmailReason = "you own a vendor bill that is due for payment."
	// reasonOwnerMoved drops a delivery whose bill now belongs to someone other than the planned recipient.
	reasonOwnerMoved = "owner_changed"
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

// renderVendorBillDigest builds the owner-facing reminder, earliest due date first.
func renderVendorBillDigest(company string, items []VendorBillDoc, today time.Time) services.Email {
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
		Reason:     vendorBillEmailReason,
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
		if !equalFold(doc.OwnerEmail, g.RecipientEmail) {
			dropped[it.OccurrenceKey] = reasonOwnerMoved
			continue
		}
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
	email := renderVendorBillDigest(profile.CompanyName, live, env.Today)
	earliest := live[0]
	for _, d := range live[1:] {
		if d.DueDate.Before(earliest.DueDate) {
			earliest = d
		}
	}
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{UserID: earliest.OwnerIdentityID, Email: earliest.OwnerEmail}},
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
