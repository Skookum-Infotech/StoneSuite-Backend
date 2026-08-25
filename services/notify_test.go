package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestSendNotification_PostsToCorrectPath(t *testing.T) {
	var gotPath string
	var gotBody NotificationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	err := SendNotification(context.Background(), NotificationRequest{
		TenantID:   "tenant-1",
		Recipients: []RecipientTarget{{UserID: "user-1"}},
		EventType:  "document.sent",
		Resource:   "invoice",
		ResourceID: "inv-1",
		Title:      "Invoice INV-1 sent",
		Attachments: []NotifyAttachment{
			{FileName: "INV-1.pdf", ContentType: "application/pdf", Content: []byte("%PDF-1.4")},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "/api/notifications/internal", gotPath)
	assert.Equal(t, "tenant-1", gotBody.TenantID)
	require.Len(t, gotBody.Attachments, 1)
	assert.Equal(t, "INV-1.pdf", gotBody.Attachments[0].FileName)
}

func TestSendNotification_NotConfigured_ReturnsError(t *testing.T) {
	config.AppConfig = config.Config{}
	err := SendNotification(context.Background(), NotificationRequest{})
	assert.Error(t, err)
}
