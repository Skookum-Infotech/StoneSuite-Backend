package controllers

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// InventoryUnitPhotoOps handles the picture of one physical slab.
//
// Routes:
//
//	GET    /api/tenant/inventory/units/{uuid}/photo — short-lived URL of the photo
//	PUT    /api/tenant/inventory/units/{uuid}/photo — raw image bytes (JPEG, PNG, WEBP)
//	DELETE /api/tenant/inventory/units/{uuid}/photo — remove it
//
// The bytes go to the tenant's R2 bucket; only the key is stored on the unit
// (inventory_slab.slab_photo_key). With no R2 configured the upload answers 503,
// like the company logo and record attachments.
type InventoryUnitPhotoOps struct {
	units *InventoryUnitOps
	r2    *storage.Client // nil when R2 is not configured
}

// NewInventoryUnitPhotoOps constructs the handler group.
func NewInventoryUnitPhotoOps(r2 *storage.Client) *InventoryUnitPhotoOps {
	return &InventoryUnitPhotoOps{units: NewInventoryUnitOps(), r2: r2}
}

const (
	// maxUnitPhotoBytes bounds the upload body. A phone photo of a slab is a few MB.
	maxUnitPhotoBytes = 10 * 1024 * 1024
	// maxUnitPhotoPixels guards the browser against a tiny file that decodes huge.
	maxUnitPhotoPixels = 80_000_000
	// unitPhotoURLTTL is how long a presigned photo URL stays valid.
	unitPhotoURLTTL = 10 * time.Minute
)

// unitPhotoFormats maps a decoded image format to its stored content type and key
// extension. GIF is left out on purpose: a slab photo is never animated.
var unitPhotoFormats = map[string]struct{ contentType, ext string }{
	"jpeg": {"image/jpeg", "jpg"},
	"png":  {"image/png", "png"},
	"webp": {"image/webp", "webp"},
}

// presignUnitPhoto returns a short-lived GET URL for a stored photo key, or "" when
// there is no photo or the bucket cannot sign it.
func presignUnitPhoto(r *http.Request, r2 *storage.Client, key string) string {
	if key == "" || r2 == nil {
		return ""
	}
	u, err := r2.PresignGet(r.Context(), key, unitPhotoURLTTL)
	if err != nil {
		slog.Warn("failed to presign slab photo URL", "error", err)
		return ""
	}
	return u
}

// Get GET /api/tenant/inventory/units/{uuid}/photo
func (h *InventoryUnitPhotoOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.units.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	key, err := inventory.UnitPhotoKey(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load unit photo.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"photoUrl": presignUnitPhoto(r, h.r2.WithBucket(tenant.R2Bucket), key),
	})
}

// Upload PUT /api/tenant/inventory/units/{uuid}/photo
func (h *InventoryUnitPhotoOps) Upload(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.units.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	r2 := h.r2.WithBucket(tenant.R2Bucket)
	if r2 == nil {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUnitPhotoBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "Failed to read upload.")
		return
	}
	contentType, ext, msg := checkUnitPhoto(body)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}

	uuid := r.PathValue("uuid")
	oldKey, err := inventory.UnitPhotoKey(r.Context(), pool, uuid)
	if err != nil {
		inventoryFail(w, err, "Failed to load unit photo.")
		return
	}
	// A new key per upload, so a replaced photo is never served from a cache.
	newKey := fmt.Sprintf("inventory/units/%s/photo-%d.%s", uuid, time.Now().UnixNano(), ext)
	if err := r2.Put(r.Context(), newKey, contentType, body); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to store photo.")
		return
	}
	if err := inventory.SetUnitPhotoKey(r.Context(), pool, uuid, newKey, resolveEmployeeID(r, identityID)); err != nil {
		// The row did not take the key: do not leave the object orphaned.
		if delErr := r2.Delete(r.Context(), newKey); delErr != nil {
			slog.Warn("failed to delete orphaned slab photo", "error", delErr)
		}
		inventoryFail(w, err, "Failed to save photo reference.")
		return
	}
	if oldKey != "" && oldKey != newKey {
		if err := r2.Delete(r.Context(), oldKey); err != nil {
			slog.Warn("failed to delete replaced slab photo", "error", err, "tenant", tenant.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "photoUrl": presignUnitPhoto(r, r2, newKey)})
}

// Delete DELETE /api/tenant/inventory/units/{uuid}/photo
func (h *InventoryUnitPhotoOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.units.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	uuid := r.PathValue("uuid")
	oldKey, err := inventory.UnitPhotoKey(r.Context(), pool, uuid)
	if err != nil {
		inventoryFail(w, err, "Failed to load unit photo.")
		return
	}
	if err := inventory.SetUnitPhotoKey(r.Context(), pool, uuid, "", resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to remove photo.")
		return
	}
	if r2 := h.r2.WithBucket(tenant.R2Bucket); r2 != nil && oldKey != "" {
		if err := r2.Delete(r.Context(), oldKey); err != nil {
			slog.Warn("failed to delete slab photo object", "error", err, "tenant", tenant.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// checkUnitPhoto validates an upload: non-empty, within the size cap, a real JPEG,
// PNG or WEBP, and not absurdly large once decoded. It returns the content type
// and key extension to store it under, or a user-facing message when it is refused.
func checkUnitPhoto(body []byte) (contentType, ext, msg string) {
	if len(body) == 0 {
		return "", "", "No image data received."
	}
	if len(body) > maxUnitPhotoBytes {
		return "", "", "The photo is too large. Keep it under 10 MB."
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return "", "", "That file is not a readable image. Use a JPEG, PNG or WEBP photo."
	}
	f, ok := unitPhotoFormats[format]
	if !ok {
		return "", "", "Use a JPEG, PNG or WEBP photo."
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxUnitPhotoPixels {
		return "", "", "The photo's dimensions are too large."
	}
	return f.contentType, f.ext, ""
}
