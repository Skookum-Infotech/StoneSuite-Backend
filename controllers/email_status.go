package controllers

import (
	"context"
	"log/slog"
	"net/http"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// This file holds the glue between list handlers and services.EmailStatuses:
// one batched lookup per request, a worst-of summary per row, and JSON views
// that add the emailStatus… keys without disturbing a row's existing shape.
// Every helper is fail-open — a missing index reads as "unknown".

// emailStatusIndex maps a notify notification id to what notify reported for it.
type emailStatusIndex map[string]services.EmailDeliveryStatus

// loadEmailStatuses asks notify about every id in idLists with one batched
// lookup. Callers pass only ids from rows they have already loaded under their
// normal auth chain; the lookup always names the tenant.
func loadEmailStatuses(ctx context.Context, tenantID string, idLists ...[]string) emailStatusIndex {
	var all []string
	for _, ids := range idLists {
		all = append(all, ids...)
	}
	return emailStatusIndex(services.EmailStatuses(ctx, tenantID, all))
}

// requestEmailStatuses is loadEmailStatuses for a tenant-scoped request: the
// tenant comes from the request context. Outside a tenant request it returns an
// empty index (rows then read as unknown) rather than failing the page.
func requestEmailStatuses(r *http.Request, idLists ...[]string) emailStatusIndex {
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		return emailStatusIndex{}
	}
	return loadEmailStatuses(r.Context(), tenant.ID, idLists...)
}

// summary reduces one send's notification ids to a client-facing summary.
func (idx emailStatusIndex) summary(ids []string) services.EmailSummary {
	return services.SummarizeEmail(ids, idx)
}

// withEmailSummary adds a summary's JSON keys to a map view and returns it.
// Optional keys are left out when empty, matching EmailSummary's omitempty tags.
func withEmailSummary(view map[string]any, s services.EmailSummary) map[string]any {
	view["emailStatus"] = s.Status
	if s.At != nil {
		view["emailStatusAt"] = s.At
	}
	if s.Message != "" {
		view["emailStatusMessage"] = s.Message
	}
	if len(s.Recipients) > 0 {
		view["emailRecipients"] = s.Recipients
	}
	return view
}

// documentSendView is a send-history row plus its real email outcome.
type documentSendView struct {
	workflow.DocumentSend
	services.EmailSummary
}

// documentSendViews attaches each send's email summary. The result is never
// nil, so an empty history serialises as [] rather than null.
func documentSendViews(sends []workflow.DocumentSend, idx emailStatusIndex) []documentSendView {
	out := make([]documentSendView, 0, len(sends))
	for _, s := range sends {
		out = append(out, documentSendView{DocumentSend: s, EmailSummary: idx.summary(s.NotifyNotificationIDs)})
	}
	return out
}

// userInviteView is a staff-invite row plus its real email outcome. The
// embedded UserInvite has no JSON tags, so its keys stay PascalCase (the UI
// depends on that); the added keys are camelCase.
type userInviteView struct {
	tenancy.UserInvite
	services.EmailSummary
}

// userInviteViews attaches each invite's email summary.
func userInviteViews(invites []tenancy.UserInvite, idx emailStatusIndex) []userInviteView {
	out := make([]userInviteView, 0, len(invites))
	for _, inv := range invites {
		out = append(out, userInviteView{UserInvite: inv, EmailSummary: idx.summary(inv.NotifyNotificationIDs)})
	}
	return out
}

// tenantInviteViews renders platform invites (via inviteView) plus their email summary.
func tenantInviteViews(invites []tenancy.Invite, idx emailStatusIndex) []map[string]any {
	out := make([]map[string]any, 0, len(invites))
	for _, inv := range invites {
		out = append(out, withEmailSummary(inviteView(inv), idx.summary(inv.NotifyNotificationIDs)))
	}
	return out
}

// notifyIDsOf collects the notification ids of rows for one batched lookup.
func notifyIDsOf[T any](rows []T, ids func(T) []string) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, ids(r))
	}
	return out
}

// persistNotifyIDs stores the notify ids of a just-sent email on its invite row
// so its real delivery status can be looked up later. Best-effort by design:
// the email has already gone, so a persistence failure is logged, never
// returned. Nothing is stored when the send failed or notify returned no ids.
func persistNotifyIDs(ctx context.Context, what, rowID string, res services.NotificationResult, sendErr error,
	set func(context.Context, string, []string) error) {
	if sendErr != nil || len(res.NotificationIDs) == 0 {
		return
	}
	if err := set(ctx, rowID, res.NotificationIDs); err != nil {
		slog.WarnContext(ctx, what+": persist notify ids failed (non-fatal)", "row_id", rowID, "error", err)
	}
}
