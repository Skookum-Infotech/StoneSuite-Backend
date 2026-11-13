package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// Takeover-limit alert email: sent to the customer's administrators the first
// time a user hits the daily cap in a day.
const (
	takeoverAlertEvent    = "user.takeover_limit"
	takeoverAlertResource = "user"
	takeoverAlertLink     = "/config/users"
	takeoverAlertTimeout  = 5 * time.Second
	takeoverAlertDateFmt  = "2006-01-02"
	emailChannel          = "email"
)

// Messages for a refused takeover.
const (
	msgTakeoverLimited = "Too many sign-ins from different devices today. Contact your administrator."
	msgTakeoverWaitFmt = "Your account was just signed in on another device. Try again in %d minute%s."
)

// cooldownMessage is the cooldown refusal text for the time left.
func cooldownMessage(wait time.Duration) string {
	minutes := int(math.Ceil(wait.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	plural := "s"
	if minutes == 1 {
		plural = ""
	}
	return fmt.Sprintf(msgTakeoverWaitFmt, minutes, plural)
}

// refuseOverLimit answers 429 when moving the sign-in now would break the
// customer's cooldown or daily cap, and reports whether it did. It is the
// early check, made before the prompt is shown; StartSession makes the
// binding one under the per-user lock. A failure to read the history refuses
// too (500): the limits must not silently stop applying.
func refuseOverLimit(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, st sessionStart) bool {
	if st.limits == (tenancy.TakeoverLimits{}) {
		return false
	}
	now := time.Now()
	hist, err := cp.MovesSince(r.Context(), st.identity.ID, now.Add(-tenancy.MoveWindow))
	if err != nil {
		slog.Error("session gate: takeover history", "identity", st.identity.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Login failed.")
		return true
	}
	refusal := tenancy.TakeoverRefusal(st.limits, hist, now)
	if refusal == nil {
		return false
	}
	writeLimitRefusal(w, r, st, refusal)
	return true
}

// writeLimitRefusal writes the 429 for a refused move. Hitting the daily cap
// also alerts the customer's administrators (once per user per day).
func writeLimitRefusal(w http.ResponseWriter, r *http.Request, st sessionStart, refusal *tenancy.TakeoverLimitError) {
	if refusal.Code == tenancy.CodeTakeoverCooldown {
		logSecurityEvent(r, "session_takeover_cooldown", "identity", st.identity.ID,
			"retry_after_seconds", int(refusal.RetryAfter.Seconds()))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"success": false, "code": refusal.Code, "message": cooldownMessage(refusal.RetryAfter),
			"retryAfterSeconds": int(math.Ceil(refusal.RetryAfter.Seconds())),
		})
		return
	}
	logSecurityEvent(r, "session_takeover_limit", "identity", st.identity.ID,
		"count", refusal.Count, "cap", st.limits.DailyCap)
	alertTakeoverLimit(r.Context(), st, refusal.Count, time.Now())
	failWithCode(w, http.StatusTooManyRequests, msgTakeoverLimited, refusal.Code)
}

// alertTakeoverLimit emails the customer's administrators that a user hit the
// daily takeover cap. Best-effort: a failure is logged, never shown to the
// user being refused. The idempotency key (user + day) makes notify send it
// once a day however often the cap is hit again.
func alertTakeoverLimit(ctx context.Context, st sessionStart, count int, now time.Time) {
	if st.pool == nil || st.tenantID == "" {
		slog.Warn("takeover limit alert: skipped, workspace not resolved", "identity", st.identity.ID)
		return
	}
	req, err := takeoverAlertRequest(ctx, st.pool, st, count, now)
	if err != nil {
		slog.Warn("takeover limit alert: build", "identity", st.identity.ID, "error", err)
		return
	}
	if len(req.Recipients) == 0 {
		slog.Warn("takeover limit alert: no active super admins to email", "identity", st.identity.ID)
		return
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), takeoverAlertTimeout)
	defer cancel()
	res, err := services.SendNotificationWithResult(sendCtx, req)
	if err != nil {
		slog.Warn("takeover limit alert: send", "identity", st.identity.ID, "error", err)
		return
	}
	slog.Info("takeover limit alert sent", "identity", st.identity.ID, "notify_ids", res.NotificationIDs)
}

// takeoverAlertRequest builds the alert to the workspace's active super admins.
func takeoverAlertRequest(ctx context.Context, pool *pgxpool.Pool, st sessionStart, count int, now time.Time) (services.NotificationRequest, error) {
	admins, err := userstore.ListActiveSuperAdmins(ctx, pool)
	if err != nil {
		return services.NotificationRequest{}, fmt.Errorf("load admins: %w", err)
	}
	recipients := make([]services.RecipientTarget, 0, len(admins))
	for _, a := range admins {
		recipients = append(recipients, services.RecipientTarget{UserID: a.IdentityID, Email: a.Email, Name: a.FullName})
	}
	who := st.identity.FullName
	if who == "" {
		who = st.identity.Email
	}
	title := "Sign-in blocked: " + who + " switched devices too often"
	body := fmt.Sprintf("%s (%s) moved their sign-in to another device %d times in the last 24 hours, "+
		"the daily limit for your workspace. Further switches are blocked until tomorrow. "+
		"If this login is being shared, review the account.", who, st.identity.Email, count)
	return services.NotificationRequest{
		TenantID:       st.tenantID,
		Recipients:     recipients,
		ActorUserID:    st.identity.ID,
		EventType:      takeoverAlertEvent,
		Resource:       takeoverAlertResource,
		ResourceID:     st.identity.ID,
		Title:          title,
		Body:           body,
		Link:           takeoverAlertLink,
		Channels:       []string{emailChannel},
		IdempotencyKey: "takeover-limit:" + st.identity.ID + ":" + now.UTC().Format(takeoverAlertDateFmt),
		Email: &services.Email{
			Preheader: title + ".", Badge: "Security", Icon: services.IconLock,
			// No per-recipient greeting: that would split this into one call
			// per admin sharing one idempotency key, and notify would then
			// deliver it to the first admin only.
			Heading: "Sign-in blocked", Greet: false,
			Paragraphs: []services.Paragraph{{services.Text(body)}},
			Details: []services.Detail{
				{Label: "User", Value: who + " (" + st.identity.Email + ")"},
				{Label: "Device switches today", Value: fmt.Sprintf("%d", count)},
			},
			ActionURL: services.AppURL(takeoverAlertLink), ActionLabel: "Review users",
		},
	}, nil
}
