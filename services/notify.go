package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/config"
)

// notifyClient bounds every call to stonesuite-notify. Without it (the old
// http.DefaultClient) a slow or cold-starting notify service could hang a
// user's document-send request for as long as the browser kept the
// connection open. 30s covers notify's ~1-2s scale-to-zero cold start plus a
// PDF-attachment upload with comfortable margin.
var notifyClient = &http.Client{Timeout: 30 * time.Second}

// RecipientTarget specifies a user to notify.
type RecipientTarget struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	// Name is the recipient's display name, used only to fill the email's
	// "Hello {name}," greeting (see personalize); never sent to notify.
	Name string `json:"-"`
}

// NotificationRequest is the payload sent to the notify service.
type NotificationRequest struct {
	TenantID    string             `json:"tenantId"`
	Recipients  []RecipientTarget  `json:"recipients"`
	ActorUserID string             `json:"actorUserId,omitempty"`
	EventType   string             `json:"eventType"`
	Resource    string             `json:"resource"`
	ResourceID  string             `json:"resourceId"`
	Title       string             `json:"title"`
	Body        string             `json:"body,omitempty"`
	Link        string             `json:"link,omitempty"`
	Channels    []string           `json:"channels,omitempty"`
	Attachments []NotifyAttachment `json:"attachments,omitempty"`
	// Email is the dynamic content of the email delivery. SendNotification
	// renders it through the one shared template (see RenderEmail) and sends
	// the result as notify's emailBodyHtml; nil falls back to a default built
	// from Title/Body/Link, so every email uses the same template.
	Email *Email `json:"-"`
}

// notifyPayload is the wire body of a create call: the request plus the
// email HTML rendered from its Email content.
type notifyPayload struct {
	NotificationRequest
	EmailBodyHTML string `json:"emailBodyHtml,omitempty"`
}

// NotifyAttachment is a single file attached to a notification's email
// delivery, base64-encoded on the wire by SendNotification.
type NotifyAttachment struct {
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	Content     []byte `json:"-"`
}

// MarshalJSON encodes Content as base64 under "contentBase64", matching
// what stonesuite-notify's POST /api/notifications/internal decodes.
func (a NotifyAttachment) MarshalJSON() ([]byte, error) {
	type wire struct {
		FileName      string `json:"fileName"`
		ContentType   string `json:"contentType"`
		ContentBase64 string `json:"contentBase64"`
	}
	return json.Marshal(wire{
		FileName:      a.FileName,
		ContentType:   a.ContentType,
		ContentBase64: base64.StdEncoding.EncodeToString(a.Content),
	})
}

// NotificationResult is what the notify service reports back from a create
// call: the id of each notification row it wrote, one per recipient. A
// caller that needs to check async delivery status later (via notify's
// GET /api/notifications/{id}/deliveries) persists these against its own
// record; callers that don't just use SendNotification and ignore it.
type NotificationResult struct {
	NotificationIDs []string
}

// SendNotification POSTs an event to the notify service.
func SendNotification(ctx context.Context, req NotificationRequest) error {
	_, err := SendNotificationWithResult(ctx, req)
	return err
}

// SendNotificationWithResult is SendNotification plus the ids of the
// notification rows the service created, parsed from its response. A
// response body that can't be parsed is not an error — the notifications
// were still created — the id list just comes back empty.
func SendNotificationWithResult(ctx context.Context, req NotificationRequest) (NotificationResult, error) {
	cfg := config.AppConfig
	if cfg.NotifyURL == "" || cfg.NotifyAPIKey == "" {
		return NotificationResult{}, fmt.Errorf("notify service not configured")
	}

	var result NotificationResult
	for _, part := range personalize(req) {
		res, err := postNotification(ctx, cfg, part)
		result.NotificationIDs = append(result.NotificationIDs, res.NotificationIDs...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// personalize splits a request so each recipient's greeting carries their own
// name: notify stores one email body per create call, so recipients whose
// names differ need separate calls. A request with no greeting, a fixed
// RecipientName, or a single shared name goes out unchanged in one call.
func personalize(req NotificationRequest) []NotificationRequest {
	if req.Email == nil || !req.Email.Greet || req.Email.RecipientName != "" || len(req.Recipients) == 0 {
		return []NotificationRequest{req}
	}
	shared := req.Recipients[0].Name
	same := true
	for _, r := range req.Recipients[1:] {
		if r.Name != shared {
			same = false
			break
		}
	}
	if same {
		e := *req.Email
		e.RecipientName = shared
		req.Email = &e
		return []NotificationRequest{req}
	}
	parts := make([]NotificationRequest, 0, len(req.Recipients))
	for _, r := range req.Recipients {
		part := req
		e := *req.Email
		e.RecipientName = r.Name
		part.Email = &e
		part.Recipients = []RecipientTarget{r}
		parts = append(parts, part)
	}
	return parts
}

// postNotification renders req's email through the shared template and makes
// one create call to the notify service.
func postNotification(ctx context.Context, cfg config.Config, req NotificationRequest) (NotificationResult, error) {
	emailHTML, err := req.EmailHTML()
	if err != nil {
		return NotificationResult{}, fmt.Errorf("render notification email: %w", err)
	}
	payload, err := json.Marshal(notifyPayload{NotificationRequest: req, EmailBodyHTML: emailHTML})
	if err != nil {
		return NotificationResult{}, fmt.Errorf("marshal notification: %w", err)
	}

	url := fmt.Sprintf("%s/api/notifications/internal", strings.TrimRight(cfg.NotifyURL, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return NotificationResult{}, fmt.Errorf("build notify request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// stonesuite-notify's /api/notifications/internal is gated by
	// middleware.RequireInternalSecret, which checks X-Internal-Secret
	// against a single shared secret (INTERNAL_SERVICE_SECRET) -- not a
	// scoped X-Api-Key. NotifyAPIKey holds that shared secret's value; only
	// the header name here needs to match notify's actual auth model.
	httpReq.Header.Set("X-Internal-Secret", cfg.NotifyAPIKey)

	resp, err := notifyClient.Do(httpReq)
	if err != nil {
		return NotificationResult{}, fmt.Errorf("execute notify request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return NotificationResult{}, fmt.Errorf("notify service returned %d: %s", resp.StatusCode, body)
	}

	// { "success": true, "data": { "notifications": [ { "id": "..." }, ... ] } }
	var decoded struct {
		Data struct {
			Notifications []struct {
				ID string `json:"id"`
			} `json:"notifications"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return NotificationResult{}, nil // created OK; we just don't get ids back
	}
	ids := make([]string, 0, len(decoded.Data.Notifications))
	for _, n := range decoded.Data.Notifications {
		if n.ID != "" {
			ids = append(ids, n.ID)
		}
	}
	return NotificationResult{NotificationIDs: ids}, nil
}
