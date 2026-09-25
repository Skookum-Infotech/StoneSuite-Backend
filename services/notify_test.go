package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestSendNotification_PostsToCorrectPath(t *testing.T) {
	var gotPath string
	var gotHeader string
	var gotBody NotificationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Internal-Secret")
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
	// stonesuite-notify's RequireInternalSecret middleware checks this exact
	// header name (middleware/auth.go) -- X-Api-Key would 401 in production.
	assert.Equal(t, "nk_dev_test_secret", gotHeader)
	assert.Equal(t, "tenant-1", gotBody.TenantID)
	require.Len(t, gotBody.Attachments, 1)
	assert.Equal(t, "INV-1.pdf", gotBody.Attachments[0].FileName)
}

func TestSendNotificationWithResult_ParsesNotificationIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-1"},{"id":"n-2"}]}}`))
	}))
	defer server.Close()

	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendNotificationWithResult(context.Background(), NotificationRequest{
		TenantID:   "tenant-1",
		Recipients: []RecipientTarget{{Email: "a@x.com"}, {Email: "b@x.com"}},
		EventType:  "document.sent", Resource: "invoice", ResourceID: "inv-1", Title: "sent",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"n-1", "n-2"}, res.NotificationIDs)
}

func TestSendNotificationWithResult_UnparseableBodyIsNotAnError(t *testing.T) {
	// The notifications were still created; we just can't correlate them
	// later. Must not fail the caller's document-send over it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated) // empty body
	}))
	defer server.Close()

	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendNotificationWithResult(context.Background(), NotificationRequest{
		TenantID: "t", Recipients: []RecipientTarget{{Email: "a@x.com"}},
		EventType: "e", Resource: "r", ResourceID: "id", Title: "t",
	})
	require.NoError(t, err)
	assert.Empty(t, res.NotificationIDs)
}

func TestSendNotification_RendersEmailThroughSharedTemplate(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret", EmailBrandName: "StoneSuite"}

	err := SendNotification(context.Background(), NotificationRequest{
		TenantID:   "tenant-1",
		Recipients: []RecipientTarget{{Email: "customer@example.com"}},
		EventType:  "document.sent",
		Resource:   "salesorder",
		ResourceID: "so-1",
		Title:      "Sales Order SO-1 sent",
		Channels:   []string{"email"},
		Email:      &Email{Badge: "Document Sent", Heading: "Sales Order SO-1", Paragraphs: []Paragraph{{Text("Please find your sales order attached.")}}},
	})
	require.NoError(t, err)

	body, _ := gotBody["emailBodyHtml"].(string)
	assert.True(t, strings.HasPrefix(body, "<!DOCTYPE html>"), "the rendered template goes out as emailBodyHtml")
	assert.Contains(t, body, ">DOCUMENT SENT<")
	assert.Contains(t, body, "Please find your sales order attached.")
	assert.NotContains(t, gotBody, "Email", "the content struct itself is not sent on the wire")
	assert.Equal(t, "customer@example.com", gotBody["recipients"].([]any)[0].(map[string]any)["email"], "reaches the intended recipient")
}

func TestSendNotification_NotConfigured_ReturnsError(t *testing.T) {
	config.AppConfig = config.Config{}
	err := SendNotification(context.Background(), NotificationRequest{})
	assert.Error(t, err)
}

func TestSendNotification_SlowNotifyService_TimesOutInsteadOfHanging(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never respond until the test tears down
	}))
	defer server.Close()
	defer close(block)

	orig := notifyClient
	notifyClient = &http.Client{Timeout: 100 * time.Millisecond}
	t.Cleanup(func() { notifyClient = orig })

	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	done := make(chan error, 1)
	go func() {
		done <- SendNotification(context.Background(), NotificationRequest{TenantID: "t"})
	}()

	select {
	case err := <-done:
		assert.Error(t, err, "a hanging notify service must surface as an error, not a nil success")
	case <-time.After(2 * time.Second):
		t.Fatal("SendNotification did not return — the notify client has no effective timeout")
	}
}

func TestPersonalize(t *testing.T) {
	two := []RecipientTarget{{Email: "a@x.com", Name: "Ann"}, {Email: "b@x.com", Name: "Bob"}}
	tests := []struct {
		name      string
		req       NotificationRequest
		wantParts int
		wantNames []string
	}{
		{"no email content: one call", NotificationRequest{Recipients: two}, 1, []string{""}},
		{"no greeting: one call", NotificationRequest{Recipients: two, Email: &Email{}}, 1, []string{""}},
		{"fixed name: one call", NotificationRequest{Recipients: two, Email: &Email{Greet: true, RecipientName: "Pat"}}, 1, []string{"Pat"}},
		{"shared name: one call", NotificationRequest{Recipients: []RecipientTarget{{Email: "a@x.com", Name: "Ann"}}, Email: &Email{Greet: true}}, 1, []string{"Ann"}},
		{"different names: one call each", NotificationRequest{Recipients: two, Email: &Email{Greet: true}}, 2, []string{"Ann", "Bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := personalize(tt.req)
			require.Len(t, parts, tt.wantParts)
			for i, p := range parts {
				name := ""
				if p.Email != nil {
					name = p.Email.RecipientName
				}
				assert.Equal(t, tt.wantNames[i], name)
			}
			if tt.wantParts > 1 {
				assert.Equal(t, []RecipientTarget{two[0]}, parts[0].Recipients)
				assert.Equal(t, []RecipientTarget{two[1]}, parts[1].Recipients)
				assert.Empty(t, tt.req.Email.RecipientName, "the caller's Email is not mutated")
			}
		})
	}
}

func TestSendNotificationWithResult_GreetsEachRecipientByNameAndAggregatesIDs(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-` + strconv.Itoa(len(bodies)) + `"}]}}`))
	}))
	defer server.Close()
	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendNotificationWithResult(context.Background(), NotificationRequest{
		Recipients: []RecipientTarget{{Email: "a@x.com", Name: "Ann"}, {Email: "b@x.com", Name: "Bob"}},
		Channels:   []string{"email"},
		Email:      &Email{Greet: true, Badge: "Approval Needed", Heading: "Invoice INV-1"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"n-1", "n-2"}, res.NotificationIDs)
	require.Len(t, bodies, 2)
	assert.Contains(t, bodies[0]["emailBodyHtml"], ">Hello Ann,<")
	assert.Contains(t, bodies[1]["emailBodyHtml"], ">Hello Bob,<")
	assert.NotContains(t, bodies[0]["recipients"].([]any)[0], "name", "names never go on the wire")
}
