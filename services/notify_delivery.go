package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stonesuite-backend/config"
)

// stonesuite-notify answers a create call (201) as soon as the notification is
// queued; the email itself is sent by its background worker a moment later, so
// a 2xx from SendNotification says nothing about whether the mail went out.
// ConfirmEmailDelivery closes that gap for handlers that must tell the user the
// truth: it polls notify's delivery log for the email channel until the first
// attempt has an outcome.
const (
	// deliveryPollInterval is how often the delivery log is re-read.
	deliveryPollInterval = 500 * time.Millisecond
	// deliveryWaitTimeout bounds the whole wait. notify's worker polls every
	// 2s and a send is capped at 10s, so a healthy send settles well inside it.
	deliveryWaitTimeout = 8 * time.Second
)

// Delivery states and channel as reported by notify's delivery log
// (stonesuite-notify/deliveries/types.go).
const (
	notifyChannelEmail       = "email"
	notifyStatusSent         = "sent"
	notifyStatusRetrying     = "retrying"
	notifyStatusFailed       = "failed"
	notifyStatusSkipped      = "skipped"
	deliveryLogPathFormat    = "%s/api/notifications/%s/deliveries"
	deliveryLogBodyLimit     = 1 << 20
	msgEmailFailed           = "The email could not be delivered. Please try again, or contact support if the problem continues."
	msgEmailPending          = "The email is taking longer than usual to send. It may still arrive shortly — check again in a minute before resending."
	msgEmailSkipped          = "Email delivery is turned off for this recipient."
	deliveryDetailNoIDs      = "notify returned no notification ids to check"
	deliveryDetailNoEmailRow = "notify recorded no email delivery for the notification"
)

// EmailStatus is what a caller can truthfully say about one email at the end of
// the request that sent it.
type EmailStatus string

const (
	// EmailStatusNone means no email was attempted (nothing to report).
	EmailStatusNone EmailStatus = ""
	// EmailStatusSent means the email provider accepted the message.
	EmailStatusSent EmailStatus = "sent"
	// EmailStatusFailed means the send failed: notify was unreachable or
	// rejected the request, or the provider refused the first attempt. notify
	// keeps retrying a refused delivery, but the caller must not claim success.
	EmailStatusFailed EmailStatus = "failed"
	// EmailStatusPending means the email was queued but no attempt had an
	// outcome within the wait window (or the delivery log could not be read).
	EmailStatusPending EmailStatus = "pending"
	// EmailStatusSkipped means the recipient's preferences turned email off.
	EmailStatusSkipped EmailStatus = "skipped"
)

// EmailOutcome is the result of sending one email through notify.
type EmailOutcome struct {
	Status EmailStatus
	// Detail is the provider/notify diagnostic. It is for server logs only —
	// it can name the sending domain or an API-key problem — and must never be
	// returned to a client; use UserMessage for that.
	Detail string
}

// Attempted reports whether an email send was made at all.
func (o EmailOutcome) Attempted() bool { return o.Status != EmailStatusNone }

// Sent reports whether the provider accepted the email.
func (o EmailOutcome) Sent() bool { return o.Status == EmailStatusSent }

// UserMessage returns a client-safe explanation of why the email was not sent,
// or "" when it was (or none was attempted).
func (o EmailOutcome) UserMessage() string {
	switch o.Status {
	case EmailStatusFailed:
		return msgEmailFailed
	case EmailStatusPending:
		return msgEmailPending
	case EmailStatusSkipped:
		return msgEmailSkipped
	default:
		return ""
	}
}

// ConfirmEmailDelivery turns the result of a Send…WithResult call into an
// honest EmailOutcome. A send error is a failure outright; otherwise it waits
// (bounded by deliveryWaitTimeout) for notify's worker to make its first
// attempt and reports how that went.
func ConfirmEmailDelivery(ctx context.Context, tenantID string, res NotificationResult, sendErr error) EmailOutcome {
	if sendErr != nil {
		return EmailOutcome{Status: EmailStatusFailed, Detail: sendErr.Error()}
	}
	return awaitEmailDelivery(ctx, config.AppConfig, tenantID, res.NotificationIDs, deliveryPollInterval, deliveryWaitTimeout)
}

// deliveryRecord is the subset of one notify delivery-log row this package reads.
type deliveryRecord struct {
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	LastError string `json:"lastError"`
}

// emailProgress is the state of one notification's email delivery.
type emailProgress struct {
	settled bool
	status  EmailStatus
	detail  string
}

// classifyEmail reads one notification's delivery rows and says whether its
// email has an outcome yet. A refused first attempt shows up as "retrying"
// (notify reschedules it) and is reported as a failure right away — the caller
// cannot wait out the retry schedule.
func classifyEmail(rows []deliveryRecord) emailProgress {
	for _, row := range rows {
		if row.Channel != notifyChannelEmail {
			continue
		}
		switch row.Status {
		case notifyStatusSent:
			return emailProgress{settled: true, status: EmailStatusSent}
		case notifyStatusRetrying, notifyStatusFailed:
			return emailProgress{settled: true, status: EmailStatusFailed, detail: row.LastError}
		case notifyStatusSkipped:
			return emailProgress{settled: true, status: EmailStatusSkipped}
		default: // pending, processing: no attempt outcome yet
			return emailProgress{}
		}
	}
	// notify writes the email row in the same call that answers "created", and
	// records "skipped" explicitly when a preference turns email off — so no
	// email row at all means this log cannot say what happened. That is
	// "unverified", never "turned off"; and since the row will not appear later,
	// do not spend the wait window looking for it.
	return emailProgress{settled: true, status: EmailStatusPending, detail: deliveryDetailNoEmailRow}
}

// awaitEmailDelivery polls the delivery log of every notification id until all
// have an email outcome or the timeout passes, then folds them into one result:
// any failure wins, then anything still unsettled, then sent over skipped.
func awaitEmailDelivery(ctx context.Context, cfg config.Config, tenantID string, ids []string, interval, timeout time.Duration) EmailOutcome {
	if len(ids) == 0 {
		return EmailOutcome{Status: EmailStatusPending, Detail: deliveryDetailNoIDs}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	progress := make(map[string]emailProgress, len(ids))
	lastErr := ""
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		for _, id := range ids {
			if progress[id].settled {
				continue
			}
			rows, err := fetchDeliveries(ctx, cfg, tenantID, id)
			if err != nil {
				// A read cut short by our own deadline is just the window
				// closing, not a diagnostic worth reporting.
				if ctx.Err() == nil {
					lastErr = err.Error()
				}
				continue
			}
			progress[id] = classifyEmail(rows)
		}
		if allSettled(ids, progress) {
			break
		}
		select {
		case <-ctx.Done():
			return foldProgress(ids, progress, lastErr)
		case <-ticker.C:
		}
	}
	return foldProgress(ids, progress, lastErr)
}

// allSettled reports whether every id has an outcome, or any has already failed
// (a failure decides the result, so there is no reason to keep waiting).
func allSettled(ids []string, progress map[string]emailProgress) bool {
	for _, id := range ids {
		p := progress[id]
		if p.settled && p.status == EmailStatusFailed {
			return true
		}
	}
	for _, id := range ids {
		if !progress[id].settled {
			return false
		}
	}
	return true
}

// foldProgress reduces per-notification progress to a single EmailOutcome.
func foldProgress(ids []string, progress map[string]emailProgress, lastErr string) EmailOutcome {
	var sent, unsettled int
	pendingDetail := lastErr
	for _, id := range ids {
		p := progress[id]
		switch {
		case !p.settled:
			unsettled++
		case p.status == EmailStatusFailed:
			return EmailOutcome{Status: EmailStatusFailed, Detail: p.detail}
		case p.status == EmailStatusPending:
			// Read fine but unverifiable (see classifyEmail).
			unsettled++
			if p.detail != "" {
				pendingDetail = p.detail
			}
		case p.status == EmailStatusSent:
			sent++
		}
	}
	switch {
	case unsettled > 0:
		return EmailOutcome{Status: EmailStatusPending, Detail: pendingDetail}
	case sent > 0:
		return EmailOutcome{Status: EmailStatusSent}
	default:
		return EmailOutcome{Status: EmailStatusSkipped}
	}
}

// fetchDeliveries reads one notification's delivery log from notify's
// internal-secret-gated GET /api/notifications/{id}/deliveries.
func fetchDeliveries(ctx context.Context, cfg config.Config, tenantID, notificationID string) ([]deliveryRecord, error) {
	if cfg.NotifyURL == "" || cfg.NotifyAPIKey == "" {
		return nil, fmt.Errorf("notify service not configured")
	}
	endpoint := fmt.Sprintf(deliveryLogPathFormat, strings.TrimRight(cfg.NotifyURL, "/"), url.PathEscape(notificationID))
	q := url.Values{"tenantId": {tenantID}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build delivery log request: %w", err)
	}
	req.Header.Set("X-Internal-Secret", cfg.NotifyAPIKey)

	resp, err := notifyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read delivery log: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, deliveryLogBodyLimit))
		return nil, fmt.Errorf("delivery log returned %d: %s", resp.StatusCode, body)
	}

	// { "success": true, "data": { "deliveries": [ { "channel", "status", "lastError" }, ... ] } }
	var decoded struct {
		Data struct {
			Deliveries []deliveryRecord `json:"deliveries"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, deliveryLogBodyLimit)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode delivery log: %w", err)
	}
	return decoded.Data.Deliveries, nil
}
