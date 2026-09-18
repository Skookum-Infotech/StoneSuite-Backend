package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG decoder
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/middleware"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// CompanyProfileOps groups the tenant-scoped Company Info handlers -- the
// tenant's own company name/address (Configuration -> Company Info), kept
// distinct from a CRM Lead/Prospect/Customer's address or a vendor's.
type CompanyProfileOps struct {
	r2 *storage.Client // nil when R2 is not configured (graceful degradation)
}

// NewCompanyProfileOps constructs the handler group. r2 may be nil when R2
// credentials are absent; the logo upload/delete endpoints return 503 in
// that case (matching AttachmentOps's existing convention).
func NewCompanyProfileOps(r2 *storage.Client) *CompanyProfileOps {
	return &CompanyProfileOps{r2: r2}
}

// maxLogoSizeBytes bounds the logo upload body.
const maxLogoSizeBytes = 2 * 1024 * 1024 // 2 MB

// logoR2Key is the tenant's single logo object -- every upload overwrites
// it, so a format switch (e.g. JPEG -> PNG) never leaves an orphaned object
// behind. The stored bytes are always re-encoded to PNG (decodeLogoAsPNG),
// so this fixed .png key is always accurate.
const logoR2Key = "company/logo.png"

// authorize resolves the caller, checks company_profile:action, and returns
// the tenant pool + tenant (needed for the tenant's R2 bucket). On failure
// it writes the response and returns ok=false.
func (h *CompanyProfileOps) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, *tenancy.Tenant, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, nil, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCompanyProfile, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, nil, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCompanyProfile), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" the company profile.")
		return nil, nil, false
	}
	return pool, tenant, true
}

// GetProfile GET /api/tenant/company-profile
func (h *CompanyProfileOps) GetProfile(w http.ResponseWriter, r *http.Request) {
	pool, tenant, ok := h.authorize(w, r, authz.ActionRead)
	if !ok {
		return
	}
	profile, err := companyprofile.Get(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load company profile.")
		return
	}
	var logoURL string
	if profile.LogoKey != "" {
		if r2 := h.r2.WithBucket(tenant.R2Bucket); r2 != nil {
			if u, err := r2.PresignGet(r.Context(), profile.LogoKey, 60*time.Second); err == nil {
				logoURL = u
			} else {
				slog.Warn("failed to presign logo preview URL", "error", err, "tenant", tenant.ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "companyProfile": profile, "logoUrl": logoURL})
}

// UpdateProfile PUT /api/tenant/company-profile
func (h *CompanyProfileOps) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var profile companyprofile.Profile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := companyprofile.Upsert(r.Context(), pool, profile); err != nil {
		var ve companyprofile.ValidationError
		if errors.As(err, &ve) {
			fail(w, http.StatusBadRequest, ve.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to save company profile.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "companyProfile": profile})
}

// UploadLogo PUT /api/tenant/company-profile/logo. Body is the raw image
// bytes (PNG or JPEG); returns 503 when R2 isn't configured for this tenant.
func (h *CompanyProfileOps) UploadLogo(w http.ResponseWriter, r *http.Request) {
	pool, tenant, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	r2 := h.r2.WithBucket(tenant.R2Bucket)
	if r2 == nil {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLogoSizeBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "Failed to read upload.")
		return
	}
	pngBytes, err := decodeLogoAsPNG(body)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r2.Put(r.Context(), logoR2Key, "image/png", pngBytes); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to store logo.")
		return
	}
	if err := companyprofile.SetLogoKey(r.Context(), pool, logoR2Key); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save logo reference.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// DeleteLogo DELETE /api/tenant/company-profile/logo. Best-effort deletes
// the R2 object (mirrors AttachmentOps.DeleteAttachment).
func (h *CompanyProfileOps) DeleteLogo(w http.ResponseWriter, r *http.Request) {
	pool, tenant, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	if err := companyprofile.SetLogoKey(r.Context(), pool, ""); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to remove logo.")
		return
	}
	if r2 := h.r2.WithBucket(tenant.R2Bucket); r2 != nil {
		if err := r2.Delete(r.Context(), logoR2Key); err != nil {
			slog.Warn("failed to delete logo object from R2", "error", err, "tenant", tenant.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// decodeLogoAsPNG validates body as a decodable PNG or JPEG under
// maxLogoSizeBytes and re-encodes it as PNG. Re-encoding (rather than
// storing the original bytes) means the stored object is always at the
// same fixed .png key regardless of what format was uploaded.
func decodeLogoAsPNG(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, errors.New("no image data received")
	}
	if len(body) > maxLogoSizeBytes {
		return nil, fmt.Errorf("logo exceeds the %d MB limit", maxLogoSizeBytes/1024/1024)
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, errors.New("logo must be a valid PNG or JPEG image")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, errors.New("failed to process logo image")
	}
	return buf.Bytes(), nil
}
