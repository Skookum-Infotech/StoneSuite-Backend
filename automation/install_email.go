package automation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	installEventType = "automation.install_tomorrow"
	// installPortalRouteFormat is the customer portal page for the order the installation belongs to.
	// The portal has no page for an appointment, and the customer already tracks the job through the order.
	installPortalRouteFormat = "/sales/sales_order/%s"
	installActionLabel       = "View your order"
	installClockLayout       = "3:04 PM"
	installDayLayout         = "Mon, Jan 2"
	installResourceOrder     = "sales_order"
)

// installDB is what the renderer reads through: queries and the company profile.
type installDB interface {
	Querier
	companyprofile.Querier
}

// installTiming words a visit's date relative to the tenant-local today.
func installTiming(days int) string {
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days > 1:
		return fmt.Sprintf("in %d days", days)
	}
	return ""
}

// installWhen renders the visit's start in its own time zone ("Tue, Jan 2, 8:00 AM" or
// "Tue, Jan 2, all day"); an unknown zone falls back to fallback.
func installWhen(r InstallRow, fallback *time.Location) string {
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil || loc == nil {
		loc = fallback
	}
	start := r.StartsAt.In(loc)
	if r.AllDay {
		return start.Format(installDayLayout) + ", all day"
	}
	return start.Format(installDayLayout) + ", " + start.Format(installClockLayout)
}

// installHeading is the title of the email and the in-app notification: it names the day when every
// visit is on the same day, and is otherwise generic.
func installHeading(items []InstallRow, today time.Time) string {
	if len(items) == 1 {
		switch installTiming(arDaysFromToday(items[0].LocalStart, today)) {
		case "today":
			return "Your installation is today"
		case "tomorrow":
			return "Your installation is tomorrow"
		}
		return "Your installation is coming up"
	}
	return "Your installations are coming up"
}

// installBody is the in-app text: the visit's date and time, or a count.
func installBody(items []InstallRow, fallback *time.Location) string {
	if len(items) == 1 {
		return "Installation scheduled for " + installWhen(items[0], fallback) + "."
	}
	return fmt.Sprintf("%d installations are coming up.", len(items))
}

// renderInstallEmail builds the customer-facing reminder, soonest visit first. portalLink is the app
// link for customers with a portal login; empty omits the button (an email without a link is better
// than one a customer cannot open).
func renderInstallEmail(company string, items []InstallRow, today time.Time, fallback *time.Location, portalLink string) services.Email {
	sorted := append([]InstallRow(nil), items...)
	for i := 1; i < len(sorted); i++ { // insertion sort by start: the list is tiny and this keeps the input untouched
		for j := i; j > 0 && sorted[j].StartsAt.Before(sorted[j-1].StartsAt); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	from := "us"
	if company != "" {
		from = company
	}
	var lead services.Paragraph
	if len(sorted) == 1 {
		lead = services.Paragraph{services.Text("This is a reminder from "), services.Bold(from),
			services.Text(" that your installation is scheduled for "), services.Bold(installWhen(sorted[0], fallback)), services.Text(".")}
	} else {
		lead = services.Paragraph{services.Text("This is a reminder from "), services.Bold(from),
			services.Text(fmt.Sprintf(" about your %d upcoming installations.", len(sorted)))}
	}
	var details []services.Detail
	for _, it := range sorted {
		when := installWhen(it, fallback)
		if t := installTiming(arDaysFromToday(it.LocalStart, today)); t != "" {
			when += " (" + t + ")"
		}
		label := "Installation"
		if it.SalesOrderNo != "" {
			label += " for order " + it.SalesOrderNo
		}
		details = append(details, services.Detail{Label: label, Value: when})
		if it.Address != "" {
			details = append(details, services.Detail{Label: "Address", Value: it.Address})
		}
	}
	heading := installHeading(sorted, today)
	email := services.Email{
		Preheader:  heading,
		Badge:      heading,
		Icon:       services.IconDocClock,
		Heading:    heading,
		Greet:      true,
		Paragraphs: []services.Paragraph{lead, {services.Text("If you need to change the time, please contact us.")}},
		Details:    details,
		Reason:     "you have an installation scheduled with " + from + ".",
	}
	if portalLink != "" {
		if email.ActionURL = services.AppURL(portalLink); email.ActionURL != "" {
			email.ActionLabel = installActionLabel
		}
	}
	return email
}

// installRenderer renders install reminders from live data.
type installRenderer struct {
	pool installDB
	deps Deps
}

// Render re-checks each installation live (still scheduled, same day, customer still reachable at the
// planned address), drops the ones that no longer apply with their reason, and builds one email for
// the rest. When the customer has a portal login the same notification also lands in their in-app feed.
func (r installRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	dropped := map[string]string{}
	var (
		kept []Delivery
		live []InstallRow
	)
	for _, it := range g.Items {
		row, found, err := loadInstall(ctx, r.pool, it.RecordID)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		reason := reasonVoided
		if found {
			reason = installReason(row, it.Anchor, g.RecipientEmail)
		}
		if reason != "" {
			dropped[it.OccurrenceKey] = reason
			continue
		}
		kept = append(kept, it)
		live = append(live, row)
	}
	if len(kept) == 0 {
		return services.NotificationRequest{}, nil, dropped, nil
	}
	profile, err := companyprofile.Get(ctx, r.pool)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("install email company profile: %w", err)
	}

	target := services.RecipientTarget{Email: g.RecipientEmail, Name: strings.TrimSpace(live[0].CustomerName)}
	channels := []string{arChannelMail}
	link := ""
	if r.deps.Portal != nil {
		id, found, err := r.deps.Portal(ctx, env.Tenant.ID, live[0].CustomerUUID)
		switch {
		case err != nil:
			// The email still goes; only the in-app copy is lost, and the next reminder tries again.
			slog.WarnContext(ctx, "install reminder: portal lookup failed, sending email only",
				"tenant", env.Tenant.ID, "customer", live[0].CustomerUUID, "error", err)
		case found:
			target.UserID = id.IdentityID
			if strings.TrimSpace(id.Name) != "" {
				target.Name = strings.TrimSpace(id.Name)
			}
			channels = append(channels, expiryChannelApp)
			link = fmt.Sprintf(installPortalRouteFormat, live[0].SalesOrderUUID)
		}
	}

	email := renderInstallEmail(profile.CompanyName, live, env.Today, env.Loc, link)
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{target},
		EventType:  installEventType,
		Resource:   installResourceOrder,
		ResourceID: live[0].SalesOrderUUID,
		Title:      installHeading(live, env.Today),
		Body:       installBody(live, env.Loc),
		Link:       link,
		Email:      &email,
		Channels:   channels,
	}, kept, dropped, nil
}
