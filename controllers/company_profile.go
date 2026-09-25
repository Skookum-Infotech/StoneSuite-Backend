package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	"image/png"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/webp" // register WEBP decoder

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
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "companyProfile": profileResponse(profile), "logoUrl": logoURL})
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
	// Answer with what is stored: an update that omitted paymentDetails keeps
	// the stored ones, so echoing the request would misreport them.
	saved, err := companyprofile.Get(r.Context(), pool)
	if err != nil {
		slog.Warn("failed to reload company profile after save", "error", err)
		saved = &profile
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "companyProfile": profileResponse(saved)})
}

// UploadLogo PUT /api/tenant/company-profile/logo. Body is the raw image
// bytes (PNG, JPEG, GIF, WEBP, or SVG); returns 503 when R2 isn't configured
// for this tenant.
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

// decodeLogoAsPNG validates body as a decodable PNG, JPEG, GIF, WEBP, or SVG
// image under maxLogoSizeBytes and re-encodes it as PNG. Re-encoding (rather
// than storing the original bytes) means the stored object is always at the
// same fixed .png key regardless of what format was uploaded.
func decodeLogoAsPNG(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, errors.New("no image data received")
	}
	if len(body) > maxLogoSizeBytes {
		return nil, fmt.Errorf("logo exceeds the %d MB limit", maxLogoSizeBytes/1024/1024)
	}

	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		// SVG is vector, not a registered image.Decode raster format -- it
		// needs its own sniff-and-rasterize path rather than a trusted
		// Content-Type header (the request body is what's authoritative).
		if !looksLikeSVG(body) {
			return nil, errors.New("logo must be a valid PNG, JPEG, GIF, WEBP, or SVG image")
		}
		img, err = rasterizeSVG(body)
		if err != nil {
			return nil, fmt.Errorf("logo SVG could not be processed: %w", err)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, cropToContent(img)); err != nil {
		return nil, errors.New("failed to process logo image")
	}
	return buf.Bytes(), nil
}

// looksLikeSVG sniffs the body for an SVG document. SVG isn't in
// image.Decode's registered-format table (it's XML text, not a raster
// codec), so it needs content sniffing rather than format dispatch --
// checked only after image.Decode has already rejected the body, so this
// never overrides a real raster decode.
func looksLikeSVG(body []byte) bool {
	head := bytes.TrimLeft(body, "\xef\xbb\xbf \t\r\n")
	if len(head) > 512 {
		head = head[:512]
	}
	lower := bytes.ToLower(head)
	if bytes.HasPrefix(lower, []byte("<svg")) {
		return true
	}
	// Allow a leading XML prolog/doctype before the <svg> root element.
	return bytes.HasPrefix(lower, []byte("<?xml")) && bytes.Contains(lower, []byte("<svg"))
}

// svgRasterMaxPx bounds the rasterized SVG's longer edge. An untrusted SVG
// can declare an arbitrarily large viewBox; rendering that at face value
// would let a small upload demand a runaway amount of memory for what is
// ultimately a small header/PDF logo.
const svgRasterMaxPx = 1024

// rasterizeSVG parses and rasterizes an SVG document into a raster
// image.Image, scaled (preserving aspect ratio) to fit within
// svgRasterMaxPx. oksvg/rasterx parse attacker-controlled markup, so a
// panic from malformed input is recovered into a normal error instead of
// crashing the request.
func rasterizeSVG(data []byte) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("invalid SVG: %v", r)
		}
	}()

	icon, parseErr := oksvg.ReadIconStream(bytes.NewReader(data))
	if parseErr != nil {
		return nil, parseErr
	}

	w, h := icon.ViewBox.W, icon.ViewBox.H
	if w <= 0 || h <= 0 {
		w, h = svgRasterMaxPx, svgRasterMaxPx
	}
	scale := 1.0
	if longer := math.Max(w, h); longer > svgRasterMaxPx {
		scale = svgRasterMaxPx / longer
	}
	outW, outH := int(w*scale), int(h*scale)
	if outW < 1 {
		outW = 1
	}
	if outH < 1 {
		outH = 1
	}

	icon.SetTarget(0, 0, float64(outW), float64(outH))
	rgba := image.NewRGBA(image.Rect(0, 0, outW, outH))
	scanner := rasterx.NewScannerGV(outW, outH, rgba, rgba.Bounds())
	raster := rasterx.NewDasher(outW, outH, scanner)
	icon.Draw(raster, 1.0)
	return rgba, nil
}

// cropToContent returns the smallest sub-image containing every non-blank
// pixel in img. Uploaded logo exports commonly carry large blank or
// transparent padding around the actual mark (e.g. a wide wordmark centered
// on a tall square canvas); left uncropped, that padding scales down along
// with the mark and the logo becomes a near-invisible sliver inside the PDF
// header's fixed-size box. Returns img unchanged if it has no non-blank
// pixels (fully blank) or the content already fills the canvas.
func cropToContent(img image.Image) image.Image {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if isBlankishPixel(r, g, bl, a) {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if minX > maxX || minY > maxY {
		return img // fully blank; nothing to crop
	}
	if minX == b.Min.X && minY == b.Min.Y && maxX == b.Max.X-1 && maxY == b.Max.Y-1 {
		return img // content already fills the canvas
	}
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	si, ok := img.(subImager)
	if !ok {
		return img
	}
	return si.SubImage(image.Rect(minX, minY, maxX+1, maxY+1))
}

// isBlankishPixel treats a fully (or near-fully) transparent pixel, or a
// near-white opaque pixel, as background padding rather than logo content.
// r/g/b/a are alpha-premultiplied 16-bit values as returned by
// image.Color.RGBA().
func isBlankishPixel(r, g, b, a uint32) bool {
	if a>>8 < 10 {
		return true
	}
	return r>>8 > 245 && g>>8 > 245 && b>>8 > 245
}
