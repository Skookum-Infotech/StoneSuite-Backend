package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/config"
)

// ConfirmEmailDelivery (notify_delivery.go) answers "did the first attempt
// work?" at request time. This file answers the later question — what did the
// email provider actually report (delivered, delayed, bounced, …) — by asking
// stonesuite-notify, which receives Resend's webhooks and is the one place that
// state lives. Every lookup here fails open: a list page must render, with
// "unknown", when notify is slow, down, or not configured.
const (
	// emailStatusTimeout bounds one whole lookup (all chunks). notify's
	// scale-to-zero cold start is ~1-2s; a list page must not hang on it.
	emailStatusTimeout = 4 * time.Second
	// emailStatusChunk is notify's per-request cap on notification ids.
	emailStatusChunk     = 200
	emailStatusPath      = "%s/api/deliveries/email-status"
	emailStatusBodyLimit = 1 << 20
)

// Real-delivery states as notify reports them (stonesuite-notify
// emailevents.DeriveState). The vocabulary is closed: anything else is treated
// as EmailStateUnknown so a notify upgrade can never put an unrendered string
// in front of a user.
const (
	EmailStateQueued     = "queued"
	EmailStateSent       = "sent"
	EmailStateRetrying   = "retrying"
	EmailStateDelayed    = "delayed"
	EmailStateDelivered  = "delivered"
	EmailStateComplained = "complained"
	EmailStateBounced    = "bounced"
	EmailStateFailed     = "failed"
	EmailStateSuppressed = "suppressed"
	EmailStateSkipped    = "skipped"
	EmailStateUnknown    = "unknown"
)

// emailStatesWorstFirst orders states by how much the sender needs to know: a
// summary shows the worst state across a send's recipients, so one bounced
// address is never hidden behind two delivered ones, and "sent" (accepted, not
// yet delivered) is never reported as "delivered".
var emailStatesWorstFirst = []string{
	EmailStateBounced, EmailStateFailed, EmailStateSuppressed, EmailStateComplained,
	EmailStateRetrying, EmailStateDelayed, EmailStateUnknown, EmailStateQueued,
	EmailStateSent, EmailStateDelivered, EmailStateSkipped,
}

// emailStateMessages is the only text a client ever sees about a state. It is
// fixed on purpose: notify's Detail can name the sending domain or an API-key
// problem and stays in the server log.
var emailStateMessages = map[string]string{
	EmailStateBounced:    "The recipient's mail server rejected this email. Check the address and try again.",
	EmailStateFailed:     "The email could not be sent. Try again, or contact support if the problem continues.",
	EmailStateSuppressed: "This address is on a suppression list because earlier emails to it bounced or were reported as spam.",
	EmailStateComplained: "The recipient reported this email as spam.",
	EmailStateRetrying:   "The first attempt to send this email failed. We are trying again.",
	EmailStateDelayed:    "The recipient's mail server is slow to accept this email. We will keep trying; no action is needed yet.",
	EmailStateSkipped:    msgEmailSkipped,
}

// EmailDeliveryStatus is notify's reading of one email.
type EmailDeliveryStatus struct {
	State     string `json:"state"`
	Recipient string `json:"recipient"`
	// Detail is the provider diagnostic. Server-side only: never copy it into a response.
	Detail    string    `json:"detail"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EmailRecipient is one recipient's state inside a summary.
type EmailRecipient struct {
	Email  string `json:"email"`
	Status string `json:"status"`
}

// EmailSummary is the client-facing outcome of one send (one or more
// recipients). Embedding it in a response struct, or copying its fields into a
// map view, adds the emailStatus… keys.
type EmailSummary struct {
	Status     string           `json:"emailStatus"`
	At         *time.Time       `json:"emailStatusAt,omitempty"`
	Message    string           `json:"emailStatusMessage,omitempty"`
	Recipients []EmailRecipient `json:"emailRecipients,omitempty"`
}

func emailStateSeverity(state string) int {
	for i, s := range emailStatesWorstFirst {
		if s == state {
			return i
		}
	}
	return emailStateSeverity(EmailStateUnknown)
}

func normalizeEmailState(state string) string {
	for _, s := range emailStatesWorstFirst {
		if s == state {
			return state
		}
	}
	return EmailStateUnknown
}

// SummarizeEmail folds the per-recipient states of one send into a single
// worst-of summary. An id notify did not answer for counts as unknown, so a
// send is never reported as better than what is actually known.
func SummarizeEmail(ids []string, statuses map[string]EmailDeliveryStatus) EmailSummary {
	if len(ids) == 0 {
		return EmailSummary{Status: EmailStateUnknown}
	}
	worst, worstSeverity := "", -1
	var worstAt time.Time
	recipients := make([]EmailRecipient, 0, len(ids))
	for _, id := range ids {
		state, at := EmailStateUnknown, time.Time{}
		if st, ok := statuses[id]; ok {
			state, at = normalizeEmailState(st.State), st.UpdatedAt
			if st.Recipient != "" {
				recipients = append(recipients, EmailRecipient{Email: st.Recipient, Status: state})
			}
		}
		if severity := emailStateSeverity(state); worst == "" || severity < worstSeverity {
			worst, worstSeverity, worstAt = state, severity, at
		}
	}
	out := EmailSummary{Status: worst, Message: emailStateMessages[worst], Recipients: recipients}
	if !worstAt.IsZero() {
		out.At = &worstAt
	}
	return out
}

// EmailStatuses asks notify what actually happened to each notification's
// email. It never fails the caller: on any problem it logs and returns what it
// has (often an empty map), and SummarizeEmail reads the gaps as unknown.
func EmailStatuses(ctx context.Context, tenantID string, ids []string) map[string]EmailDeliveryStatus {
	out, err := fetchEmailStatuses(ctx, config.AppConfig, tenantID, ids)
	if err != nil {
		slog.WarnContext(ctx, "email status lookup failed; rendering without delivery status",
			"tenant", tenantID, "ids", len(ids), "error", err)
	}
	return out
}

// fetchEmailStatuses de-duplicates ids, asks notify in chunks, and returns the
// merged answers plus the first error (the map is always usable). An
// unconfigured notify is "nothing to ask", not an error: dev setups run without it.
func fetchEmailStatuses(ctx context.Context, cfg config.Config, tenantID string, ids []string) (map[string]EmailDeliveryStatus, error) {
	out := map[string]EmailDeliveryStatus{}
	distinct := distinctNonEmpty(ids)
	if len(distinct) == 0 || cfg.NotifyURL == "" || cfg.NotifyAPIKey == "" {
		return out, nil
	}

	ctx, cancel := context.WithTimeout(ctx, emailStatusTimeout)
	defer cancel()

	var firstErr error
	for start := 0; start < len(distinct); start += emailStatusChunk {
		end := min(start+emailStatusChunk, len(distinct))
		got, err := postEmailStatus(ctx, cfg, tenantID, distinct[start:end])
		for id, st := range got {
			out[id] = st
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

func distinctNonEmpty(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// postEmailStatus makes one call to notify's internal-secret-gated
// POST /api/deliveries/email-status.
func postEmailStatus(ctx context.Context, cfg config.Config, tenantID string, ids []string) (map[string]EmailDeliveryStatus, error) {
	payload, err := json.Marshal(map[string]any{"tenantId": tenantID, "notificationIds": ids})
	if err != nil {
		return nil, fmt.Errorf("marshal email status request: %w", err)
	}
	endpoint := fmt.Sprintf(emailStatusPath, strings.TrimRight(cfg.NotifyURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build email status request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", cfg.NotifyAPIKey)

	resp, err := notifyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read email status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, emailStatusBodyLimit))
		return nil, fmt.Errorf("email status returned %d: %s", resp.StatusCode, body)
	}

	// { "success": true, "data": { "statuses": { "<id>": { "state", "recipient", "detail", "updatedAt" } } } }
	var decoded struct {
		Data struct {
			Statuses map[string]EmailDeliveryStatus `json:"statuses"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, emailStatusBodyLimit)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode email status: %w", err)
	}
	return decoded.Data.Statuses, nil
}
