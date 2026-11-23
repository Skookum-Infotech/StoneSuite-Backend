package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	installEventType   = "automation.install_tomorrow"
	installTitle       = "Site visit reminder"
	installActionLabel = "Open install schedule"
	installEmailReason = "you are on the crew for a scheduled site visit."
	installRouteFormat = "/calendar/install-schedule?appointment=%s"
	installClockLayout = "3:04 PM"
	installDayLayout   = "Mon, Jan 2"
)

// installVisitLabel names a visit type for people ("Install", "Template", "Service visit").
func installVisitLabel(visitType string) string {
	switch visitType {
	case "template":
		return "Template"
	case "service":
		return "Service visit"
	}
	return "Install"
}

// installTiming words a visit's date relative to the tenant-local today.
func installTiming(days int) string {
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 1:
		return fmt.Sprintf("in %d days", days)
	}
	return fmt.Sprintf("%d days ago", -days)
}

// installBody is the in-app text for n visits.
func installBody(n int) string {
	if n == 1 {
		return "1 site visit is coming up."
	}
	return fmt.Sprintf("%d site visits are coming up.", n)
}

// installWhen renders the visit's start in its own time zone ("Tue, Jan 2, 8:00 AM"
// or "Tue, Jan 2, all day"); an unknown zone falls back to fallback.
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

// renderInstallDigest builds the crew-facing reminder, soonest visit first.
func renderInstallDigest(company string, items []InstallRow, today time.Time, fallback *time.Location) services.Email {
	sorted := append([]InstallRow(nil), items...)
	sort.SliceStable(sorted, func(a, b int) bool {
		if !sorted[a].StartsAt.Equal(sorted[b].StartsAt) {
			return sorted[a].StartsAt.Before(sorted[b].StartsAt)
		}
		return sorted[a].UUID < sorted[b].UUID
	})
	lead := services.Paragraph{services.Text("You have a site visit coming up.")}
	if len(sorted) > 1 {
		lead = services.Paragraph{services.Text("You have site visits coming up.")}
	}
	if company != "" {
		lead = append(lead, services.Text(" Sent by "), services.Bold(company), services.Text("."))
	}
	details := make([]services.Detail, 0, len(sorted))
	for _, it := range sorted {
		label := installVisitLabel(it.VisitType)
		if it.JobNumber != "" {
			label += " " + it.JobNumber
		}
		details = append(details, services.Detail{
			Label: label,
			Value: fmt.Sprintf("%s, %s (%s)", it.CustomerName, installWhen(it, fallback), installTiming(arDaysFromToday(it.LocalStart, today))),
		})
	}
	email := services.Email{
		Preheader:  installTitle,
		Badge:      installTitle,
		Icon:       services.IconDocClock,
		Heading:    installTitle,
		Greet:      true,
		Paragraphs: []services.Paragraph{lead},
		Details:    details,
		Reason:     installEmailReason,
	}
	if len(sorted) > 0 {
		if email.ActionURL = services.AppURL(fmt.Sprintf(installRouteFormat, sorted[0].UUID)); email.ActionURL != "" {
			email.ActionLabel = installActionLabel
		}
	}
	return email
}

// installRenderer renders install reminder digests from live visit data.
type installRenderer struct{ pool Querier2 }

// Querier2 is what the renderer needs: a Querier that companyprofile can also read through.
type Querier2 interface {
	Querier
	companyprofile.Querier
}

// Render re-checks each visit live for its recipient, drops ineligible ones
// with their reason and builds one in-app plus email digest for the rest.
func (r installRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	var kept []Delivery
	var live []InstallRow
	dropped := map[string]string{}
	for _, it := range g.Items {
		ok, reason, row, err := installEligible(ctx, r.pool, it.RecordID, g.RecipientEmail, it.Anchor)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		if !ok {
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
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("install digest company profile: %w", err)
	}
	email := renderInstallDigest(profile.CompanyName, live, env.Today, env.Loc)
	soonest := live[0]
	for _, v := range live[1:] {
		if v.StartsAt.Before(soonest.StartsAt) {
			soonest = v
		}
	}
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{UserID: soonest.IdentityID, Email: soonest.Email, Name: strings.TrimSpace(soonest.Name)}},
		EventType:  installEventType,
		Resource:   resourceInstallAppointment,
		ResourceID: soonest.UUID,
		Title:      installTitle,
		Body:       installBody(len(live)),
		Link:       fmt.Sprintf(installRouteFormat, soonest.UUID),
		Email:      &email,
		Channels:   []string{arChannelMail, expiryChannelApp},
	}, kept, dropped, nil
}
