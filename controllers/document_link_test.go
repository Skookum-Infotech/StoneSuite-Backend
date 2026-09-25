package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

func withLinkSecret(t *testing.T, secret string) {
	t.Helper()
	prev := config.AppConfig
	config.AppConfig.JWTSecret = secret
	config.AppConfig.APIBaseURL = "https://api.example"
	t.Cleanup(func() { config.AppConfig = prev })
}

func TestDocLink_RoundTrip(t *testing.T) {
	withLinkSecret(t, "s3cret")
	now := time.Now()
	tok, err := signDocLink("tenant-1", "rec-1", now)
	require.NoError(t, err)

	c, err := verifyDocLink(tok, now)
	require.NoError(t, err)
	assert.Equal(t, "tenant-1", c.TenantID)
	assert.Equal(t, "rec-1", c.RecordID)
}

func TestVerifyDocLink_Rejects(t *testing.T) {
	withLinkSecret(t, "s3cret")
	now := time.Now()
	good, err := signDocLink("tenant-1", "rec-1", now)
	require.NoError(t, err)
	payload, sig, _ := strings.Cut(good, ".")
	other, err := signDocLink("tenant-2", "rec-9", now)
	require.NoError(t, err)
	otherPayload, _, _ := strings.Cut(other, ".")

	tests := []struct {
		name  string
		token string
		at    time.Time
	}{
		{"empty", "", now},
		{"no signature", payload, now},
		{"garbage", "abc.def", now},
		{"payload swapped under old signature", otherPayload + "." + sig, now},
		{"expired", good, now.Add(docLinkTTL + time.Minute)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifyDocLink(tc.token, tc.at)
			assert.ErrorIs(t, err, errDocLinkInvalid)
		})
	}

	t.Run("signed with another secret", func(t *testing.T) {
		withLinkSecret(t, "different")
		_, err := verifyDocLink(good, now)
		assert.ErrorIs(t, err, errDocLinkInvalid)
	})
}

func TestDocLinkURL(t *testing.T) {
	withLinkSecret(t, "s3cret")
	assert.True(t, strings.HasPrefix(DocLinkURL("t", "r"), "https://api.example"+DocLinkPathPrefix))

	withLinkSecret(t, "")
	assert.Empty(t, DocLinkURL("t", "r"), "no secret, no link")
}

func TestDownloadByLink_BadTokenIs404(t *testing.T) {
	withLinkSecret(t, "s3cret")
	h := NewDocumentOps(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, DocLinkPathPrefix+"nope", nil)
	req.SetPathValue("token", "nope")
	rec := httptest.NewRecorder()
	h.DownloadByLink(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWithDownloadLink(t *testing.T) {
	e := withDownloadLink(&services.Email{Attachment: &services.AttachmentCard{FileName: "A.pdf"}}, "https://x/y")
	assert.Equal(t, "https://x/y", e.Attachment.URL)
	assert.NotPanics(t, func() { withDownloadLink(&services.Email{}, "https://x/y") })
}
