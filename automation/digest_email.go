package automation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	digestEventType   = "automation.daily_digest"
	digestTitle       = "Your daily digest"
	digestActionLabel = "Open StoneSuite"
	digestEmailReason = "you turned on the daily digest in your notification settings."
	digestNothing     = "Nothing needs your attention today."
	// digestHomeRoute is where the digest's button leads: the app's home screen.
	digestHomeRoute = "/"
)

// digestListMax caps how many individual installations or bills an email lists; the rest are counted.
const digestListMax = 5

// digestDetails turns the content into the label/value rows of the email: installations, then vendor
// bills, then the approvals waiting per module.
func digestDetails(c digestContent, today time.Time, loc *time.Location) []services.Detail {
	var rows []services.Detail
	for i, r := range c.Installs {
		if i == digestListMax {
			rows = append(rows, services.Detail{Label: "More installations", Value: fmt.Sprintf("+%d more", len(c.Installs)-digestListMax)})
			break
		}
		value := r.CustomerName
		if r.SalesOrderNo != "" {
			value = r.SalesOrderNo + " - " + r.CustomerName
		}
		rows = append(rows, services.Detail{Label: "Installation " + installWhen(r, loc), Value: value})
	}
	for i, b := range c.Bills {
		if i == digestListMax {
			rows = append(rows, services.Detail{Label: "More vendor bills", Value: fmt.Sprintf("+%d more", len(c.Bills)-digestListMax)})
			break
		}
		rows = append(rows, services.Detail{Label: "Vendor bill " + b.Number + " (" + b.VendorName + ")",
			Value: services.FormatEmailMoney("", b.Balance) + ", " + digestDueWording(b.DueDate, today)})
	}
	for _, m := range c.Approvals.Modules {
		if m.Count > 0 {
			rows = append(rows, services.Detail{Label: m.Label + " waiting for your approval", Value: fmt.Sprint(m.Count)})
		}
	}
	return rows
}

// digestDueWording says when a bill is due relative to today.
func digestDueWording(due, today time.Time) string {
	switch d := arDaysFromToday(due, today); {
	case d < 0:
		return "overdue by " + plural(-d, "day")
	case d == 0:
		return "due today"
	case d == 1:
		return "due tomorrow"
	default:
		return fmt.Sprintf("due in %d days", d)
	}
}

// plural words a count with its noun: "1 invoice", "3 invoices".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// digestBody is the in-app text for the content, e.g. "2 installations, 1 vendor bill due, 3 approvals waiting."
func digestBody(c digestContent) string {
	var parts []string
	if n := len(c.Installs); n > 0 {
		parts = append(parts, plural(n, "installation"))
	}
	if n := len(c.Bills); n > 0 {
		parts = append(parts, plural(n, "vendor bill")+" due")
	}
	if n := c.Approvals.Total; n > 0 {
		parts = append(parts, plural(n, "approval")+" waiting")
	}
	if len(parts) == 0 {
		return digestNothing
	}
	return strings.Join(parts, ", ") + "."
}

// renderDigestEmail builds the digest email; an empty content says so.
func renderDigestEmail(company string, c digestContent, today time.Time, loc *time.Location) services.Email {
	lead := services.Paragraph{services.Text("Here is what needs your attention today.")}
	if c.empty() {
		lead = services.Paragraph{services.Text(digestNothing)}
	}
	if company != "" {
		lead = append(lead, services.Text(" Sent by "), services.Bold(company), services.Text("."))
	}
	email := services.Email{
		Preheader:  digestTitle,
		Badge:      digestTitle,
		Icon:       services.IconDocClock,
		Heading:    digestTitle,
		Greet:      true,
		Paragraphs: []services.Paragraph{lead},
		Details:    digestDetails(c, today, loc),
		Reason:     digestEmailReason,
	}
	if email.ActionURL = services.AppURL(digestHomeRoute); email.ActionURL != "" {
		email.ActionLabel = digestActionLabel
	}
	return email
}

// digestRenderer renders daily digests from live data.
type digestRenderer struct{ pool digestDB }

// digestDB is what the renderer needs: reads, writes (the approval count) and the
// company profile.
type digestDB interface {
	ExecQuerier
	companyprofile.Querier
}

// Render re-reads the user's preferences and content at send time, so a digest
// the user switched off, or that has become empty, is dropped instead of sent.
func (r digestRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	dropped := map[string]string{}
	var kept []Delivery
	var rcpt DigestRecipient
	var content digestContent
	for _, it := range g.Items {
		rc, ok, err := loadDigestRecipient(ctx, r.pool, it.RecordID)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		switch {
		case !ok || !strings.EqualFold(rc.Email, g.RecipientEmail):
			dropped[it.OccurrenceKey] = reasonRecipientChanged
			continue
		case !rc.Prefs.Enabled:
			dropped[it.OccurrenceKey] = reasonDigestOff
			continue
		}
		c, err := buildDigestContent(ctx, r.pool, rc, env.Today)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		if c.empty() && rc.Prefs.SkipEmpty {
			dropped[it.OccurrenceKey] = reasonEmptyDigest
			continue
		}
		kept, rcpt, content = append(kept, it), rc, c
	}
	if len(kept) == 0 {
		return services.NotificationRequest{}, nil, dropped, nil
	}
	profile, err := companyprofile.Get(ctx, r.pool)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("digest company profile: %w", err)
	}
	email := renderDigestEmail(profile.CompanyName, content, env.Today, env.Loc)
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{UserID: rcpt.IdentityID, Email: rcpt.Email, Name: strings.TrimSpace(rcpt.Name)}},
		EventType:  digestEventType,
		Resource:   resourceDailyDigest,
		ResourceID: rcpt.UserID,
		Title:      digestTitle,
		Body:       digestBody(content),
		Link:       digestHomeRoute,
		Email:      &email,
		Channels:   []string{arChannelMail, expiryChannelApp},
	}, kept, dropped, nil
}
