package controllers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

const (
	// DocLinkPathPrefix is the public route a customer's emailed PDF link
	// points at; the signed token is the final path segment.
	DocLinkPathPrefix = "/api/public/documents/"
	// docLinkTTL is how long an emailed PDF link keeps working.
	docLinkTTL = 7 * 24 * time.Hour
	// docLinkKeyContext separates this HMAC key from every other use of the
	// JWT secret, so a signature minted elsewhere can never validate here.
	docLinkKeyContext = "document-link:v1:"
)

var errDocLinkInvalid = errors.New("invalid or expired document link")

// docLinkClaims is the signed payload of an emailed PDF link.
type docLinkClaims struct {
	TenantID string `json:"t"`
	RecordID string `json:"r"`
	Expires  int64  `json:"e"`
}

func docLinkSignature(payload string) string {
	mac := hmac.New(sha256.New, []byte(docLinkKeyContext+config.AppConfig.JWTSecret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// signDocLink returns an expiring token authorising a download of one record's
// PDF without a login.
func signDocLink(tenantID, recordID string, now time.Time) (string, error) {
	raw, err := json.Marshal(docLinkClaims{TenantID: tenantID, RecordID: recordID, Expires: now.Add(docLinkTTL).Unix()})
	if err != nil {
		return "", fmt.Errorf("marshal document link claims: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + docLinkSignature(payload), nil
}

// verifyDocLink checks a token's signature and expiry and returns its claims.
func verifyDocLink(token string, now time.Time) (docLinkClaims, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(docLinkSignature(payload))) {
		return docLinkClaims{}, errDocLinkInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return docLinkClaims{}, errDocLinkInvalid
	}
	var c docLinkClaims
	if err := json.Unmarshal(raw, &c); err != nil || c.TenantID == "" || c.RecordID == "" || now.Unix() > c.Expires {
		return docLinkClaims{}, errDocLinkInvalid
	}
	return c, nil
}

// DocLinkURL is the absolute, emailable download URL for a record's PDF ("" when
// no secret is configured or signing fails).
func DocLinkURL(tenantID, recordID string) string {
	if config.AppConfig.JWTSecret == "" {
		return ""
	}
	token, err := signDocLink(tenantID, recordID, time.Now())
	if err != nil {
		slog.Warn("sign document link failed", "error", err)
		return ""
	}
	return strings.TrimRight(config.AppConfig.APIBaseURL, "/") + DocLinkPathPrefix + token
}

// docLinkTenants resolves the tenant and database a signed link points at.
type docLinkTenants interface {
	TenantByID(ctx context.Context, id string) (*tenancy.Tenant, error)
}

// WithPublicDownload enables DownloadByLink and returns h. poolFor is
// tenancy.Router.PoolFor.
func (h *DocumentOps) WithPublicDownload(
	tenants docLinkTenants, poolFor func(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error),
) *DocumentOps {
	h.linkTenants, h.linkPool = tenants, poolFor
	return h
}

// DownloadByLink streams a record's PDF as a download to whoever holds a valid
// signed link (the customer, from the emailed PDF card). No login: the token,
// minted at send time, is the authorisation. Every failure is a bare 404 so a
// bad link reveals nothing about tenants or records.
func (h *DocumentOps) DownloadByLink(w http.ResponseWriter, r *http.Request) {
	if h.linkTenants == nil || h.linkPool == nil {
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}
	claims, err := verifyDocLink(r.PathValue("token"), time.Now())
	if err != nil {
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}
	ctx := r.Context()
	tenant, err := h.linkTenants.TenantByID(ctx, claims.TenantID)
	if err != nil || tenant.Status != tenancy.StatusActive {
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}
	pool, err := h.linkPool(ctx, tenant)
	if err != nil {
		slog.Warn("document link: tenant pool", "error", err, "tenant", claims.TenantID)
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}
	info, err := workflow.ResolveRecordAccess(ctx, pool, claims.RecordID)
	loader, ok := h.loaders[info.WorkflowKey]
	if err != nil || !ok {
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}
	doc, meta, err := loader(ctx, pool, claims.RecordID, h.sellerFromTenant(ctx, pool, tenant))
	if err != nil {
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}
	pdf, err := h.renderPDF(doc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to render PDF.")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+workflow.SanitizeFileName(meta.Number+".pdf")+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}
