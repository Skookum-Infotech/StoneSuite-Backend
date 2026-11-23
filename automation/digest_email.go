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

// digestDetails turns the content into the label/value rows of the email, one
// per non-empty section, in a fixed order.
func digestDetails(c digestContent, today time.Time) []services.Detail {
	var rows []services.Detail
	if c.Approvals > 0 {
		value := plural(c.Approvals, "record")
		if c.OldestApproval != nil {
			if days := -arDaysFromToday(*c.OldestApproval, today); days > 0 {
				value += fmt.Sprintf(", oldest waiting %s", plural(days, "day"))
			}
		}
		rows = append(rows, services.Detail{Label: "Waiting for your approval", Value: value})
	}
	if c.OverdueInvoices > 0 {
		rows = append(rows, services.Detail{Label: "Your overdue invoices",
			Value: fmt.Sprintf("%s, %s outstanding", plural(c.OverdueInvoices, "invoice"), services.FormatEmailMoney("", c.OverdueTotal))})
	}
	if c.ExpiringQuotes > 0 {
		rows = append(rows, services.Detail{Label: fmt.Sprintf("Your quotes expiring within %d days", digestExpiryHorizonDays),
			Value: plural(c.ExpiringQuotes, "quote")})
	}
	if c.ExpiringEstimates > 0 {
		rows = append(rows, services.Detail{Label: fmt.Sprintf("Your estimates expiring within %d days", digestExpiryHorizonDays),
			Value: plural(c.ExpiringEstimates, "estimate")})
	}
	return rows
}

// plural words a count with its noun: "1 invoice", "3 invoices".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// digestBody is the in-app text for the content.
func digestBody(c digestContent) string {
	n := 0
	for _, v := range []int{c.Approvals, c.OverdueInvoices, c.ExpiringQuotes, c.ExpiringEstimates} {
		if v > 0 {
			n++
		}
	}
	switch n {
	case 0:
		return digestNothing
	case 1:
		return "1 item needs your attention."
	}
	return fmt.Sprintf("%d items need your attention.", n)
}

// renderDigestEmail builds the digest email; an empty content says so.
func renderDigestEmail(company string, c digestContent, today time.Time) services.Email {
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
		Details:    digestDetails(c, today),
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
	email := renderDigestEmail(profile.CompanyName, content, env.Today)
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
