package automation

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	arEventType   = "automation.ar_reminder"
	arEmailTitle  = "Payment reminder"
	arEmailReason = "you are the billing contact for an outstanding invoice"
	arDateLayout  = "Jan 2, 2006"
	arChannelMail = "email"
)

// arDaysFromToday returns due minus today in whole calendar days, comparing
// only the Y/M/D of each (the DATE scan is UTC, today is tenant-local).
func arDaysFromToday(due, today time.Time) int {
	dy, dm, dd := due.Date()
	ty, tm, td := today.Date()
	d := time.Date(dy, dm, dd, 0, 0, 0, 0, time.UTC)
	t := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(d.Sub(t).Hours() / hoursPerDay)
}

// arTiming words an invoice's due date relative to today.
func arTiming(days int) string {
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

// renderARDigest builds the plain transactional reminder listing items by due
// date (oldest first) with live balances; an item's empty Link omits its link.
func renderARDigest(company string, items []ARInvoice, today time.Time) services.Email {
	sorted := append([]ARInvoice(nil), items...)
	sort.SliceStable(sorted, func(a, b int) bool {
		if !sameDay(sorted[a].Anchor, sorted[b].Anchor) {
			return sorted[a].Anchor.Before(sorted[b].Anchor)
		}
		return sorted[a].Number < sorted[b].Number
	})
	from := "us"
	if company != "" {
		from = company
	}
	lead := services.Paragraph{services.Text("This is a reminder about an outstanding invoice from "), services.Bold(from), services.Text(".")}
	if len(sorted) > 1 {
		lead = services.Paragraph{services.Text(fmt.Sprintf("This is a reminder about %d outstanding invoices from ", len(sorted))), services.Bold(from), services.Text(".")}
	}
	paras := []services.Paragraph{lead}
	details := make([]services.Detail, 0, len(sorted))
	for _, it := range sorted {
		details = append(details, services.Detail{
			Label: "Invoice " + it.Number,
			Value: fmt.Sprintf("Due %s (%s), balance $%s", it.Anchor.Format(arDateLayout), arTiming(arDaysFromToday(it.Anchor, today)), it.Balance),
		})
		if it.Link != "" {
			paras = append(paras, services.Paragraph{services.Link("View invoice "+it.Number+" (PDF)", it.Link)})
		}
	}
	paras = append(paras, services.Paragraph{services.Text("If you have already sent payment, please disregard this message.")})
	return services.Email{
		Preheader:  arEmailTitle,
		Badge:      arEmailTitle,
		Icon:       services.IconDocClock,
		Heading:    arEmailTitle,
		Greet:      true,
		Paragraphs: paras,
		Details:    details,
		Reason:     arEmailReason,
	}
}

// arRenderer renders AR digests from live invoice data.
type arRenderer struct {
	pool *pgxpool.Pool
	deps Deps
}

// Render re-checks each item live, drops ineligible ones with their reason and
// builds one digest for the rest, showing the live balance.
func (r arRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	var kept []Delivery
	var live []ARInvoice
	dropped := map[string]string{}
	for _, it := range g.Items {
		ok, reason, inv, err := arEligible(ctx, r.pool, it.RecordID, it.Anchor, env.Today)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		if !ok {
			dropped[it.OccurrenceKey] = reason
			continue
		}
		if r.deps.LinkURL != nil {
			inv.Link = r.deps.LinkURL(env.Tenant.ID, inv.UUID)
		}
		kept = append(kept, it)
		live = append(live, inv)
	}
	if len(kept) == 0 {
		return services.NotificationRequest{}, nil, dropped, nil
	}
	profile, err := companyprofile.Get(ctx, r.pool)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("ar digest company profile: %w", err)
	}
	email := renderARDigest(profile.CompanyName, live, env.Today)
	first := live[0].UUID
	for _, inv := range live {
		if inv.Anchor.Before(live[0].Anchor) {
			first = inv.UUID
		}
	}
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{Email: g.RecipientEmail}},
		EventType:  arEventType,
		Resource:   resourceInvoice,
		ResourceID: first,
		Title:      arEmailTitle,
		Body:       fmt.Sprintf("%d invoice(s) need attention.", len(live)),
		Email:      &email,
		Channels:   []string{arChannelMail},
	}, kept, dropped, nil
}
