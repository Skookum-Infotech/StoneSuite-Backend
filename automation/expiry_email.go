package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	expiryEventType   = "automation.expiry_reminder"
	expiryEmailReason = "you own a sent %s that is about to expire"
	expiryChannelApp  = "in_app"
	// reasonOwnerChanged drops a delivery whose document now belongs to someone other than the planned recipient.
	reasonOwnerChanged = "owner_changed"
)

// capitalize upper-cases the first byte of s; empty stays empty.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// expiryBody is the in-app text for n documents of the given noun.
func expiryBody(noun string, n int) string {
	if n == 1 {
		return fmt.Sprintf("1 %s is about to expire.", noun)
	}
	return fmt.Sprintf("%d %ss are about to expire.", n, noun)
}

// expiryTiming words a document's valid-until date relative to today.
func expiryTiming(days int) string {
	switch {
	case days == 0:
		return "expires today"
	case days == 1:
		return "expires in 1 day"
	case days > 1:
		return fmt.Sprintf("expires in %d days", days)
	case days == -1:
		return "expired 1 day ago"
	}
	return fmt.Sprintf("expired %d days ago", -days)
}

// expiryTitle is the heading for a noun such as "quote": "Quote expiry reminder".
func expiryTitle(noun string) string {
	if noun == "" {
		return "Expiry reminder"
	}
	return capitalize(noun) + " expiry reminder"
}

// renderExpiryDigest builds the plain owner-facing reminder listing items by
// valid-until date then number; an item's empty Link omits its link.
func renderExpiryDigest(company, noun string, items []ExpiryDoc, today time.Time) services.Email {
	sorted := append([]ExpiryDoc(nil), items...)
	sort.SliceStable(sorted, func(a, b int) bool {
		if !sameDay(sorted[a].ValidUntil, sorted[b].ValidUntil) {
			return sorted[a].ValidUntil.Before(sorted[b].ValidUntil)
		}
		return sorted[a].Number < sorted[b].Number
	})
	lead := services.Paragraph{services.Text(fmt.Sprintf("This %s is about to expire.", noun))}
	if len(sorted) > 1 {
		lead = services.Paragraph{services.Text(fmt.Sprintf("These %ss are about to expire.", noun))}
	}
	if company != "" {
		lead = append(lead, services.Text(" Sent by "), services.Bold(company), services.Text("."))
	}
	paras := []services.Paragraph{lead}
	details := make([]services.Detail, 0, len(sorted))
	label := capitalize(noun)
	for _, it := range sorted {
		details = append(details, services.Detail{
			Label: label + " " + it.Number,
			Value: fmt.Sprintf("%s, valid until %s (%s)", it.CustomerName, it.ValidUntil.Format(arDateLayout), expiryTiming(arDaysFromToday(it.ValidUntil, today))),
		})
		if it.Link != "" {
			paras = append(paras, services.Paragraph{services.Link("View "+noun+" "+it.Number, it.Link)})
		}
	}
	title := expiryTitle(noun)
	return services.Email{
		Preheader:  title,
		Badge:      title,
		Icon:       services.IconDocClock,
		Heading:    title,
		Greet:      true,
		Paragraphs: paras,
		Details:    details,
		Reason:     fmt.Sprintf(expiryEmailReason, noun),
	}
}

// expiryRenderer renders expiry digests from live document data.
type expiryRenderer struct {
	pool *pgxpool.Pool
	deps Deps
	spec expirySpec
}

// Render re-checks each item live, drops ineligible ones with their reason and
// builds one in-app plus email digest addressed to the owner for the rest.
func (r expiryRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	var kept []Delivery
	var live []ExpiryDoc
	dropped := map[string]string{}
	for _, it := range g.Items {
		ok, reason, doc, err := expiryEligible(ctx, r.pool, r.spec, it.RecordID, it.Anchor)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		if !ok {
			dropped[it.OccurrenceKey] = reason
			continue
		}
		if !strings.EqualFold(doc.OwnerEmail, g.RecipientEmail) {
			dropped[it.OccurrenceKey] = reasonOwnerChanged
			continue
		}
		if r.deps.LinkURL != nil {
			doc.Link = r.deps.LinkURL(env.Tenant.ID, doc.UUID)
		}
		kept = append(kept, it)
		live = append(live, doc)
	}
	if len(kept) == 0 {
		return services.NotificationRequest{}, nil, dropped, nil
	}
	profile, err := companyprofile.Get(ctx, r.pool)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("%s expiry digest company profile: %w", r.spec.Resource, err)
	}
	email := renderExpiryDigest(profile.CompanyName, r.spec.Resource, live, env.Today)
	earliest := live[0]
	for _, d := range live[1:] {
		if d.ValidUntil.Before(earliest.ValidUntil) {
			earliest = d
		}
	}
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{UserID: live[0].OwnerIdentityID, Email: live[0].OwnerEmail}},
		EventType:  expiryEventType,
		Resource:   r.spec.Resource,
		ResourceID: earliest.UUID,
		Title:      expiryTitle(r.spec.Resource),
		Body:       expiryBody(r.spec.Resource, len(live)),
		Email:      &email,
		Channels:   []string{arChannelMail, expiryChannelApp},
	}, kept, dropped, nil
}
