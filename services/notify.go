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

	"stonesuite-backend/config"
)

// RecipientTarget specifies a user to notify.
type RecipientTarget struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
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

// SendNotification POSTs an event to the notify service.
func SendNotification(ctx context.Context, req NotificationRequest) error {
	cfg := config.AppConfig
	if cfg.NotifyURL == "" || cfg.NotifyAPIKey == "" {
		return fmt.Errorf("notify service not configured")
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	url := fmt.Sprintf("%s/api/notifications/internal", strings.TrimRight(cfg.NotifyURL, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build notify request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", cfg.NotifyAPIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("execute notify request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("notify service returned %d: %s", resp.StatusCode, body)
	}

	return nil
}
