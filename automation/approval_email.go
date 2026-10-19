package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"stonesuite-backend/approvalchain"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/services"
)

const (
	approvalEventType = "automation.approval_stuck"
	// approvalEmailReason completes "You're receiving this because ..." for a configured approver.
	approvalEmailReason = "you are an approver who has not yet signed off on this."
	// approvalEmailReasonFallback is the variant for owner/super-admin escalations.
	approvalEmailReasonFallback = "you own this record or are a super admin, and no remaining approver can act on it."
	approvalActionLabel         = "Open in StoneSuite"
	approvalTitle               = "Approval reminder"
	approvalFallbackNote        = "No remaining approver can act on at least one of these records, so it has been escalated to its owner and the workspace super admins."
	// userByEmailSQL resolves the active user a digest group is addressed to.
	userByEmailSQL = `SELECT id::text FROM users WHERE LOWER(email) = LOWER($1) AND status = $2 ORDER BY id LIMIT 1`
)

// approvalLine is one record waiting at an approval gate. Route is the in-app route.
type approvalLine struct {
	DisplayName, Number, Route string
	DaysWaiting                int
	Fallback                   bool
}

// approvalWaiting words how long a record has waited.
func approvalWaiting(days int) string {
	if days == 1 {
		return "waiting 1 day"
	}
	return fmt.Sprintf("waiting %d days", days)
}

// approvalBody is the in-app text for n records.
func approvalBody(n int) string {
	if n == 1 {
		return "1 record is waiting for your approval."
	}
	return fmt.Sprintf("%d records are waiting for your approval.", n)
}

// renderApprovalDigest builds the transactional reminder listing records oldest first.
// The action button opens the oldest record inside the app (login required).
func renderApprovalDigest(company string, items []approvalLine, today time.Time) services.Email {
	_ = today // waiting days are precomputed per line against the tenant-local today
	sorted := append([]approvalLine(nil), items...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].DaysWaiting > sorted[b].DaysWaiting })
	lead := services.Paragraph{services.Text("This record is waiting for your approval.")}
	if len(sorted) > 1 {
		lead = services.Paragraph{services.Text("These records are waiting for your approval.")}
	}
	if company != "" {
		lead = append(lead, services.Text(" Sent by "), services.Bold(company), services.Text("."))
	}
	paras := []services.Paragraph{lead}
	fallback := false
	details := make([]services.Detail, 0, len(sorted))
	for _, it := range sorted {
		fallback = fallback || it.Fallback
		details = append(details, services.Detail{Label: it.DisplayName + " " + it.Number, Value: approvalWaiting(it.DaysWaiting)})
	}
	reason := approvalEmailReason
	if fallback {
		paras = append(paras, services.Paragraph{services.Text(approvalFallbackNote)})
		reason = approvalEmailReasonFallback
	}
	email := services.Email{
		Preheader:  approvalTitle,
		Badge:      approvalTitle,
		Icon:       services.IconDocClock,
		Heading:    approvalTitle,
		Greet:      true,
		Paragraphs: paras,
		Details:    details,
		Reason:     reason,
	}
	if len(sorted) > 0 {
		email.ActionURL = services.AppURL(sorted[0].Route)
		if email.ActionURL != "" {
			email.ActionLabel = approvalActionLabel
		}
	}
	return email
}

// approvalRenderer renders stuck-approval digests from live record data.
type approvalRenderer struct {
	q approvalDB
}

// approvalDB is what the renderer needs: Querier plus Exec (companyprofile.Querier).
type approvalDB interface {
	Querier
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// resolveRecipientUser returns the active user id for an email, "" when none.
func (r approvalRenderer) resolveRecipientUser(ctx context.Context, email string) (string, error) {
	rows, err := r.q.Query(ctx, userByEmailSQL, email, userStatusActive)
	if err != nil {
		return "", fmt.Errorf("approval digest recipient lookup: %w", err)
	}
	defer rows.Close()
	var id string
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("scan approval digest recipient: %w", err)
		}
	}
	return id, rows.Err()
}

// evaluate finds the gate the record currently sits at for the delivery's
// workflow. A record is at exactly one gate, so the first spec that does not
// report not_pending decides; all not_pending means the record left the gates.
func (r approvalRenderer) evaluate(ctx context.Context, env JobEnv, it Delivery, userID string) (string, approvalSpec, ApprovalRecord, approvalRecipient, error) {
	reason := reasonNotPending
	for _, spec := range approvalSpecs() {
		if spec.Workflow != it.Resource {
			continue
		}
		got, rec, rcpt, err := approvalState(ctx, r.q, spec, it.RecordID, it.Anchor, userID, env.Loc)
		if err != nil {
			return "", approvalSpec{}, ApprovalRecord{}, approvalRecipient{}, err
		}
		if got != reasonNotPending {
			return got, spec, rec, rcpt, nil
		}
		reason = got
	}
	return reason, approvalSpec{}, ApprovalRecord{}, approvalRecipient{}, nil
}

// Render re-checks each item live, drops ineligible ones with their reason and
// builds one email plus in-app notification for the rest.
func (r approvalRenderer) Render(ctx context.Context, env JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	userID, err := r.resolveRecipientUser(ctx, g.RecipientEmail)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, err
	}
	dropped := map[string]string{}
	if userID == "" {
		for _, it := range g.Items {
			dropped[it.OccurrenceKey] = reasonRecipientChanged
		}
		return services.NotificationRequest{}, nil, dropped, nil
	}
	seen := map[string]bool{}
	var kept []Delivery
	var lines []approvalLine
	var recipient approvalRecipient
	var oldest ApprovalRecord
	var oldestWorkflow string
	for _, it := range g.Items {
		reason, spec, rec, rcpt, err := r.evaluate(ctx, env, it, userID)
		if err != nil {
			return services.NotificationRequest{}, nil, nil, err
		}
		if reason == "" && seen[it.RecordID] {
			// A record that moved gates the same day has one live row per gate; keep the first.
			reason = reasonSuperseded
		}
		if reason != "" {
			dropped[it.OccurrenceKey] = reason
			continue
		}
		seen[it.RecordID] = true
		line := approvalLine{
			DisplayName: spec.DisplayName, Number: rec.Number, Fallback: rcpt.Fallback,
			Route:       approvalchain.RecordRoute(spec.Workflow, rec.UUID),
			DaysWaiting: max(0, -arDaysFromToday(rec.Entered, env.Today)),
		}
		if len(lines) == 0 || line.DaysWaiting > lines[0].DaysWaiting {
			oldest, oldestWorkflow, recipient = rec, spec.Workflow, rcpt
			lines = append([]approvalLine{line}, lines...)
		} else {
			lines = append(lines, line)
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		return services.NotificationRequest{}, nil, dropped, nil
	}
	profile, err := companyprofile.Get(ctx, r.q)
	if err != nil {
		return services.NotificationRequest{}, nil, nil, fmt.Errorf("approval digest company profile: %w", err)
	}
	email := renderApprovalDigest(profile.CompanyName, lines, env.Today)
	return services.NotificationRequest{
		TenantID:   env.Tenant.ID,
		Recipients: []services.RecipientTarget{{UserID: recipient.IdentityID, Email: recipient.Email, Name: strings.TrimSpace(recipient.Name)}},
		EventType:  approvalEventType,
		Resource:   oldestWorkflow,
		ResourceID: oldest.UUID,
		Title:      approvalTitle,
		Body:       approvalBody(len(kept)),
		Link:       lines[0].Route,
		Email:      &email,
		Channels:   []string{arChannelMail, expiryChannelApp},
	}, kept, dropped, nil
}
