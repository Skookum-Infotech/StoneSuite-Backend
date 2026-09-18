# Per-Tenant PDF Logo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let each tenant upload a logo (PNG/JPEG) that renders on every generated document PDF (quote/estimate/salesorder/invoice/etc.), without changing anything else about the PDF layout.

**Architecture:** Store an R2 object key on the tenant's existing `company_profile` row (Configuration → Company Info). Two new tenant-scoped endpoints upload/delete the logo, reusing the existing `company_profile` RBAC resource. `docpdf.Render`'s shared header draws the logo when `Seller.LogoPNG` is set; a missing/invalid logo always falls back to today's text-only header, never breaks rendering.

**Tech Stack:** Go stdlib `image`/`image/png`/`image/jpeg` (decode/re-encode, no new dependency), `go-pdf/fpdf` (already vendored), `storage.Client` (already-built R2 client, per-tenant bucket via `WithBucket`).

## Global Constraints

- Accept PNG/JPEG only, 2MB cap — reject WEBP/other formats with a clear 400 (per approved spec; `go-pdf/fpdf` only embeds PNG/JPEG/GIF).
- Every uploaded image is re-encoded to PNG server-side before storage, so the stored R2 object is always at the same fixed key (`company/logo.png`) regardless of what format was uploaded — no orphaned objects from format switches, and `Seller.LogoPNG` is always literally PNG bytes.
- Reuse the `company_profile` RBAC resource (`configure` for write, `read` for read) — no new `authz` catalog entry.
- **Do NOT change `sellerFromTenant`'s existing Name/Address/Phone/Email data source** (tenant control-plane `metadata` JSON). Only the logo is added as a new, additive lookup against `company_profile`. (This narrows one line of the approved design spec — the spec's text about switching the whole letterhead to `company_profile` would silently blank the Name/Address on any tenant whose `company_profile` row is still empty, which is a real regression outside the approved "just add the logo" scope. Flagging here since it deviates from the written spec.)
- A missing/broken logo (absent key, R2 error, not configured) must never fail PDF generation — log and continue text-only.
- `docpdf/layout.go`'s Bill To/Ship To/line-table/totals/footer are untouched.

---

### Task 1: Schema — `company_profile.logo_r2_key` column

**Files:**
- Modify: `database/migrations/tenant/schema.sql:8285` (end of the `company_profile` `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` block, just before the `-- Company Locations` section comment)

**Interfaces:**
- Produces: a `logo_r2_key VARCHAR(255) NOT NULL DEFAULT ''` column on `company_profile`, read by `companyprofile.Get` and written by `companyprofile.SetLogoKey` (Task 2).

- [ ] **Step 1: Add the idempotent column**

Insert this line immediately after the existing `return_addr_zip` line (schema.sql:8285) and before the `-- Company Locations` comment block:

```sql
ALTER TABLE company_profile ADD COLUMN IF NOT EXISTS logo_r2_key VARCHAR(255) NOT NULL DEFAULT '';
```

- [ ] **Step 2: Verify the file still parses as valid SQL structure (visual check)**

Confirm the line sits inside the `company_profile` ALTER block (same style/indentation as its neighbors) and does not touch the `company_location` table below it.

- [ ] **Step 3: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(schema): add company_profile.logo_r2_key column"
```

---

### Task 2: `companyprofile` package — logo key read/write

**Files:**
- Modify: `companyprofile/companyprofile.go`
- Test: `companyprofile/companyprofile_dbtest_test.go` (dbtest-tagged; requires `TEST_DATABASE_URL`, skips cleanly without it per repo convention)

**Interfaces:**
- Consumes: nothing new (same `Querier` interface already defined in this file).
- Produces:
  - `Profile.LogoKey string` (new field, `json:"logoKey"`)
  - `func SetLogoKey(ctx context.Context, q Querier, key string) error` — upserts **only** `logo_r2_key`; never touches any other column, so it is safe to call independently of `Upsert`.
  - `Get` now also returns `LogoKey` in the `Profile` it returns.

- [ ] **Step 1: Write the failing test**

Append to `companyprofile/companyprofile_dbtest_test.go`:

```go
func TestSetLogoKey_RoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// SetLogoKey must work even before any profile row exists.
	if err := SetLogoKey(ctx, pool, "company/logo.png"); err != nil {
		t.Fatalf("SetLogoKey() error = %v, want nil", err)
	}
	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.LogoKey != "company/logo.png" {
		t.Errorf("LogoKey = %q, want %q", got.LogoKey, "company/logo.png")
	}

	// Clearing (empty string) must work and must not touch other fields.
	if err := Upsert(ctx, pool, Profile{CompanyName: "Acme"}); err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	if err := SetLogoKey(ctx, pool, ""); err != nil {
		t.Fatalf("SetLogoKey(\"\") error = %v, want nil", err)
	}
	got, err = Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.LogoKey != "" {
		t.Errorf("LogoKey = %q, want empty after clear", got.LogoKey)
	}
	if got.CompanyName != "Acme" {
		t.Errorf("CompanyName = %q, want %q (SetLogoKey must not clobber other fields)", got.CompanyName, "Acme")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags dbtest ./companyprofile/... -run TestSetLogoKey_RoundTrip -v`
Expected (with `TEST_DATABASE_URL` set): FAIL with `undefined: SetLogoKey`. Without `TEST_DATABASE_URL`: SKIP (acceptable — implement anyway, this is TDD intent not a hard gate here).

- [ ] **Step 3: Add `LogoKey` to `Profile`, extend `Get`, add `SetLogoKey`**

In `companyprofile/companyprofile.go`, add the field to the `Profile` struct (after `TaxID`):

```go
	TaxID       string `json:"taxId"`
	LogoKey     string `json:"logoKey"`
```

Extend `Get`'s query and scan to include it (add `, logo_r2_key` to the `SELECT` column list, right after `tax_id`, and `&p.LogoKey` to the matching position in `Scan`):

```go
	err := q.QueryRow(ctx, `
		SELECT company_name, legal_name, industry, website, country, currency, timezone, tax_id, logo_r2_key,
		       billing_addr_line1, billing_addr_line2, billing_addr_suite, billing_addr_city, billing_addr_country, billing_addr_state, billing_addr_zip,
		       shipping_addr_line1, shipping_addr_line2, shipping_addr_suite, shipping_addr_city, shipping_addr_country, shipping_addr_state, shipping_addr_zip,
		       return_addr_line1, return_addr_line2, return_addr_suite, return_addr_city, return_addr_country, return_addr_state, return_addr_zip
		FROM company_profile WHERE id = 1`).
		Scan(
			&p.CompanyName, &p.LegalName, &p.Industry, &p.Website, &p.Country, &p.Currency, &p.Timezone, &p.TaxID, &p.LogoKey,
			&p.BillingAddress.Line1, &p.BillingAddress.Line2, &p.BillingAddress.Suite, &p.BillingAddress.City, &p.BillingAddress.Country, &p.BillingAddress.State, &p.BillingAddress.Zip,
			&p.ShippingAddress.Line1, &p.ShippingAddress.Line2, &p.ShippingAddress.Suite, &p.ShippingAddress.City, &p.ShippingAddress.Country, &p.ShippingAddress.State, &p.ShippingAddress.Zip,
			&p.ReturnAddress.Line1, &p.ReturnAddress.Line2, &p.ReturnAddress.Suite, &p.ReturnAddress.City, &p.ReturnAddress.Country, &p.ReturnAddress.State, &p.ReturnAddress.Zip,
		)
```

**Do not** add `LogoKey`/`logo_r2_key` anywhere in `Upsert` — leave `Upsert` completely unchanged. Add this new function after `Upsert`:

```go
// SetLogoKey stores (or clears, with an empty string) the tenant's logo R2
// object key without touching any other company_profile field — kept
// separate from Upsert so the logo endpoints never need to round-trip the
// rest of the profile just to change the logo.
func SetLogoKey(ctx context.Context, q Querier, key string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO company_profile (id, logo_r2_key) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET logo_r2_key = EXCLUDED.logo_r2_key, updated_at = NOW()`,
		key)
	if err != nil {
		return fmt.Errorf("set company profile logo key: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes (if `TEST_DATABASE_URL` is set)**

Run: `go test -tags dbtest ./companyprofile/... -run TestSetLogoKey_RoundTrip -v`
Expected: PASS (or SKIP if no test DB — either is fine to proceed).

- [ ] **Step 5: Run the full package test suite (no dbtest needed for this)**

Run: `go build ./companyprofile/... && go test ./companyprofile/...`
Expected: PASS (existing `TestValidate`-style tests untouched and still green; this package has no non-dbtest tests today beyond what compiles).

- [ ] **Step 6: Commit**

```bash
git add companyprofile/companyprofile.go companyprofile/companyprofile_dbtest_test.go
git commit -m "feat(companyprofile): add logo_r2_key read/write"
```

---

### Task 3: `controllers/company_profile.go` — upload/delete/preview endpoints

**Files:**
- Modify: `controllers/company_profile.go`
- Test: `controllers/company_profile_test.go` (new file — table-driven, no DB needed for the image-validation logic; handler wiring tested via the existing style of this repo's controller tests, see `controllers/crm_notify_test.go` for the pattern of testing without a live DB where feasible)

**Interfaces:**
- Consumes: `companyprofile.Get`, `companyprofile.SetLogoKey` (Task 2); `storage.Client` (`WithBucket`, `Put`, `Get`, `Delete`, `PresignGet` — all already exist, unchanged).
- Produces:
  - `NewCompanyProfileOps(r2 *storage.Client) *CompanyProfileOps` (constructor signature changes — was `NewCompanyProfileOps()`)
  - `(h *CompanyProfileOps) UploadLogo(w http.ResponseWriter, r *http.Request)` — `PUT`, raw image body
  - `(h *CompanyProfileOps) DeleteLogo(w http.ResponseWriter, r *http.Request)` — `DELETE`
  - `GetProfile`'s JSON response gains a `logoUrl` field (string, `""` when no logo)

- [ ] **Step 1: Write the failing test**

Create `controllers/company_profile_test.go`:

```go
package controllers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func samplePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func sampleJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestDecodeLogoImage(t *testing.T) {
	tests := []struct {
		name    string
		body    func(t *testing.T) []byte
		wantErr bool
	}{
		{"valid png", samplePNG, false},
		{"valid jpeg", sampleJPEG, false},
		{"garbage bytes", func(t *testing.T) []byte { return []byte("not an image") }, true},
		{"empty body", func(t *testing.T) []byte { return nil }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := decodeLogoAsPNG(tc.body(t))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(out, []byte("\x89PNG")), "output must be re-encoded as PNG")
		})
	}
}

func TestDecodeLogoImage_OversizeRejected(t *testing.T) {
	big := make([]byte, maxLogoSizeBytes+1)
	_, err := decodeLogoAsPNG(big)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./controllers/... -run TestDecodeLogoImage -v`
Expected: FAIL with `undefined: decodeLogoAsPNG` / `undefined: maxLogoSizeBytes`.

- [ ] **Step 3: Implement**

Replace the full contents of `controllers/company_profile.go`:

```go
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
		return nil, errors.New("No image data received.")
	}
	if len(body) > maxLogoSizeBytes {
		return nil, fmt.Errorf("Logo exceeds the %d MB limit.", maxLogoSizeBytes/1024/1024)
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, errors.New("Logo must be a valid PNG or JPEG image.")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, errors.New("Failed to process logo image.")
	}
	return buf.Bytes(), nil
}
```

Note: `image/png` is imported normally (used for both decode-registration and `png.Encode`); `image/jpeg` is blank-imported only to register its decoder.

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./controllers/... && go test ./controllers/... -run TestDecodeLogoImage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add controllers/company_profile.go controllers/company_profile_test.go
git commit -m "feat(controllers): add tenant logo upload/delete endpoints"
```

---

### Task 4: `docpdf` — render the logo in the header

**Files:**
- Modify: `docpdf/layout.go`
- Test: `docpdf/document_test.go`

**Interfaces:**
- Consumes: `PrintableDoc.Seller.LogoPNG []byte` (already exists, unchanged).
- Produces: no new exported surface — `drawHeader` behavior only.

- [ ] **Step 1: Write the failing test**

Add to `docpdf/document_test.go` (needs a `bytes` import for the PNG builder helper — add it to the existing `import` block alongside `"bytes"` which is already imported):

```go
func samplePNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 80))
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestDrawHeader_WithLogo_TakesMoreSpaceThanWithout(t *testing.T) {
	logo := samplePNGBytes(t)

	withoutLogo := newDoc()
	y0 := withoutLogo.GetY()
	drawHeader(withoutLogo, sampleDoc())
	heightWithout := withoutLogo.GetY() - y0

	d := sampleDoc()
	d.Seller.LogoPNG = logo
	withLogo := newDoc()
	y1 := withLogo.GetY()
	drawHeader(withLogo, d)
	heightWith := withLogo.GetY() - y1

	assert.GreaterOrEqual(t, heightWith, heightWithout, "a logo must never shrink the header")
}

func TestDrawHeader_InvalidLogoBytes_FallsBackGracefully(t *testing.T) {
	d := sampleDoc()
	d.Seller.LogoPNG = []byte("not a real image")
	pdf := newDoc()
	drawHeader(pdf, d) // must not panic or poison the document
	assert.True(t, pdf.Ok(), "an invalid logo must not break the rest of the PDF")
}
```

Add `"image"` and `"image/png"` to `docpdf/document_test.go`'s import block (`bytes` is already imported).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./docpdf/... -run TestDrawHeader -v`
Expected: FAIL — `heightWith` equals `heightWithout` (logo currently ignored entirely), since `drawHeader` doesn't look at `LogoPNG` yet.

- [ ] **Step 3: Implement**

Replace `docpdf/layout.go`'s `drawHeader` function and add a `drawLogo` helper (insert `drawLogo` right after `drawHeader`, before `drawParties`). Also add the three new constants next to the existing `const` block and add a `"bytes"` import:

```go
import (
	"bytes"
	"fmt"

	"github.com/go-pdf/fpdf"
)

const (
	marginX     = 15.0
	pageBottomY = 270.0 // A4 height 297mm minus footer zone
	fontFace    = "Helvetica"

	logoMaxW   = 35.0 // mm
	logoMaxH   = 18.0 // mm
	logoGutter = 4.0  // mm gap between logo and seller name/address text
)
```

```go
func drawHeader(pdf *fpdf.Fpdf, d PrintableDoc) {
	startY := pdf.GetY()
	titleRowH := 8.0

	nameX, nameW := marginX, 100.0
	logoBottom := startY
	if len(d.Seller.LogoPNG) > 0 {
		if w, h, ok := drawLogo(pdf, marginX, startY, d.Seller.LogoPNG); ok {
			nameX = marginX + w + logoGutter
			nameW = 100.0 - w - logoGutter
			logoBottom = startY + h
		}
	}

	pdf.SetXY(nameX, startY)
	pdf.SetFont(fontFace, "B", 16)
	pdf.Cell(nameW, titleRowH, d.Seller.Name)
	pdf.SetFont(fontFace, "B", 20)
	pdf.CellFormat(0, titleRowH, d.Kind, "", 1, "R", false, 0, "")

	pdf.SetFont(fontFace, "", 9)
	for _, ln := range []string{d.Seller.AddrLine1, d.Seller.AddrLine2, d.Seller.CityStateZip, d.Seller.Phone, d.Seller.Email} {
		if ln == "" {
			continue
		}
		pdf.SetX(nameX)
		pdf.CellFormat(nameW, 4.5, ln, "", 2, "L", false, 0, "")
	}
	sellerEndY := pdf.GetY()

	pdf.SetY(startY + titleRowH)
	pdf.SetFont(fontFace, "", 10)
	pdf.CellFormat(0, 5, "No: "+d.Number, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, "Status: "+d.Status, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, "Date: "+d.IssueDate, "", 1, "R", false, 0, "")
	if d.DueDate != "" {
		pdf.CellFormat(0, 5, "Due: "+d.DueDate, "", 1, "R", false, 0, "")
	}
	metaEndY := pdf.GetY()

	pdf.SetY(maxF(maxF(sellerEndY, metaEndY), logoBottom))
	pdf.Ln(4)
}

// drawLogo registers and draws logoPNG (always PNG bytes -- see
// decodeLogoAsPNG in controllers/company_profile.go, the only writer of
// Seller.LogoPNG) at (x, y), scaled to fit within logoMaxW x logoMaxH while
// preserving aspect ratio. Returns the drawn width/height and whether the
// image was valid and drawn; on any failure it clears fpdf's internal error
// state (fpdf poisons every subsequent call once an error is set) so an
// invalid logo degrades to the text-only header instead of breaking the
// whole document.
func drawLogo(pdf *fpdf.Fpdf, x, y float64, logoPNG []byte) (w, h float64, ok bool) {
	info := pdf.RegisterImageOptionsReader("seller-logo", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(logoPNG))
	if pdf.Err() || info == nil {
		pdf.ClearError()
		return 0, 0, false
	}
	iw, ih := info.Extent()
	if iw <= 0 || ih <= 0 {
		return 0, 0, false
	}
	scale := logoMaxW / iw
	if ih*scale > logoMaxH {
		scale = logoMaxH / ih
	}
	w, h = iw*scale, ih*scale
	pdf.ImageOptions("seller-logo", x, y, w, h, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
	if pdf.Err() {
		pdf.ClearError()
		return 0, 0, false
	}
	return w, h, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./docpdf/... -v`
Expected: PASS, including all pre-existing tests (`TestRender`, `TestDrawAddress_WrapsLongLines`, etc.) — this task must not regress the address-wrap fix already committed.

- [ ] **Step 5: Commit**

```bash
git add docpdf/layout.go docpdf/document_test.go
git commit -m "feat(docpdf): render the tenant logo in the PDF header when set"
```

---

### Task 5: Wire the logo into `sellerFromTenant`

**Files:**
- Modify: `controllers/documents.go`

**Interfaces:**
- Consumes: `companyprofile.Get` (Task 2), `storage.Client.WithBucket`/`.Get` (existing).
- Produces: `DocumentOps` gains an `r2 *storage.Client` field; `NewDocumentOps` signature changes to take it; `sellerFromTenant` becomes a method needing `ctx`/`pool` in addition to `tenant`.

- [ ] **Step 1: Update `DocumentOps` and its constructor**

In `controllers/documents.go`, add `"stonesuite-backend/storage"` to the import block, and change:

```go
type DocumentOps struct {
	loaders map[string]DocumentLoader
	sendDisabled map[string]bool
	renderPDF func(docpdf.PrintableDoc) ([]byte, error)
}

func NewDocumentOps(loaders map[string]DocumentLoader, sendDisabled map[string]bool) *DocumentOps {
	return &DocumentOps{loaders: loaders, sendDisabled: sendDisabled, renderPDF: docpdf.Render}
}
```

to:

```go
type DocumentOps struct {
	loaders map[string]DocumentLoader
	sendDisabled map[string]bool
	renderPDF func(docpdf.PrintableDoc) ([]byte, error)
	r2 *storage.Client // nil when R2 is not configured; logo lookup is skipped, not fatal
}

func NewDocumentOps(loaders map[string]DocumentLoader, sendDisabled map[string]bool, r2 *storage.Client) *DocumentOps {
	return &DocumentOps{loaders: loaders, sendDisabled: sendDisabled, renderPDF: docpdf.Render, r2: r2}
}
```

- [ ] **Step 2: Update `loadForRender`'s call site and `sellerFromTenant`**

Change the call site (was `seller := sellerFromTenant(tenant)`):

```go
	seller := h.sellerFromTenant(r.Context(), pool, tenant)
```

Replace the `sellerFromTenant` function with a method on `*DocumentOps` that adds the logo lookup as a new, additive step on top of the existing metadata-based fields (do **not** change how Name/AddrLine1/CityStateZip/Phone/Email are derived):

```go
// sellerFromTenant builds the letterhead from the tenant's display name and
// whatever company profile fields exist in its onboarding metadata JSON,
// then additively looks up a logo from company_profile if one has been
// uploaded (Configuration -> Company Info -> logo). A missing/unreadable
// logo is logged and skipped -- it must never fail document rendering.
func (h *DocumentOps) sellerFromTenant(ctx context.Context, pool *pgxpool.Pool, t *tenancy.Tenant) docpdf.Seller {
	s := sellerFromTenantMeta(t.DisplayName, t.Metadata)
	profile, err := companyprofile.Get(ctx, pool)
	if err != nil {
		slog.Warn("failed to load company profile for PDF logo lookup", "error", err, "tenant", t.ID)
		return s
	}
	if profile.LogoKey == "" {
		return s
	}
	r2 := h.r2.WithBucket(t.R2Bucket)
	if r2 == nil {
		return s
	}
	logo, err := r2.Get(ctx, profile.LogoKey)
	if err != nil {
		slog.Warn("failed to fetch tenant logo for PDF", "error", err, "tenant", t.ID)
		return s
	}
	s.LogoPNG = logo
	return s
}
```

Add `"stonesuite-backend/companyprofile"` and `"log/slog"` to the import block if not already present (`log/slog` is not currently imported in this file — check before adding to avoid a duplicate).

- [ ] **Step 3: Build**

Run: `go build ./controllers/...`
Expected: fails at this point only if `main.go`'s `NewDocumentOps` call site hasn't been updated yet — that's Task 6. If this task is done in isolation, temporarily confirm with `go vet ./controllers/... 2>&1 | grep documents.go` that the only new error is the call-site arity mismatch in `main.go`, not something inside `documents.go` itself.

- [ ] **Step 4: Commit**

```bash
git add controllers/documents.go
git commit -m "feat(controllers): fetch the tenant's logo when building PDF sellers"
```

---

### Task 6: `main.go` wiring

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Move R2 client construction earlier**

Cut this block (currently around the "Record attachments" comment, before `attachOps := controllers.NewAttachmentOps(r2Client)`):

```go
		// Record attachments (Cloudflare R2). r2Client is nil when R2 env vars are
		// absent — presign/download endpoints return 503; list/metadata still work.
		r2Client, r2Err := storage.New(config.AppConfig)
		if r2Err != nil {
			log.Printf("WARNING: R2 storage client failed to initialise: %v", r2Err)
		}
		if r2Client != nil {
			log.Println("R2 storage: configured.")
		} else {
			log.Println("R2 storage: not configured (attachment upload/download endpoints will return 503).")
		}
```

and paste it immediately before the existing `// Tenant's own Company Info` comment (so it runs before `companyProfileOps` is constructed). Leave `attachOps := controllers.NewAttachmentOps(r2Client)` and everything after it exactly where it is — `r2Client` is still in scope there, just declared earlier now.

- [ ] **Step 2: Update the `company-profile` route block**

Change:

```go
		// Tenant's own Company Info (name/address -- Configuration -> Company Info).
		companyProfileOps := controllers.NewCompanyProfileOps()
		mux.Handle("GET /api/tenant/company-profile", tenantChain(companyProfileOps.GetProfile))
		mux.Handle("PUT /api/tenant/company-profile", tenantChain(companyProfileOps.UpdateProfile))
```

to:

```go
		// Tenant's own Company Info (name/address -- Configuration -> Company Info).
		companyProfileOps := controllers.NewCompanyProfileOps(r2Client)
		mux.Handle("GET /api/tenant/company-profile", tenantChain(companyProfileOps.GetProfile))
		mux.Handle("PUT /api/tenant/company-profile", tenantChain(companyProfileOps.UpdateProfile))
		mux.Handle("PUT /api/tenant/company-profile/logo", tenantChain(companyProfileOps.UploadLogo))
		mux.Handle("DELETE /api/tenant/company-profile/logo", tenantChain(companyProfileOps.DeleteLogo))
```

- [ ] **Step 3: Update the `docOps` construction**

Change:

```go
	docOps := controllers.NewDocumentOps(docLoaders)
```

to:

```go
	docOps := controllers.NewDocumentOps(docLoaders, sendDisabled, r2Client)
```

Check the actual current call at `main.go:839` first (`go run . 2>&1 | head -30` after this edit will show the exact required arguments if `sendDisabled` isn't the right second-argument variable name in context — read the surrounding lines before editing, since `NewDocumentOps`'s second parameter name in the existing call may differ).

- [ ] **Step 4: Build the whole binary**

Run: `go build ./...`
Expected: PASS, no errors.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat(main): wire tenant logo upload/delete routes and R2 into DocumentOps"
```

---

### Task 7: Full verification pass

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: PASS.

- [ ] **Step 3: Run the full non-dbtest test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Render a real sample PDF with a logo (manual smoke check, not a unit test)**

Write a throwaway `_test.go` file (or reuse `go run` with a small `main` in a scratch dir) that calls `docpdf.Render` with a `Seller.LogoPNG` set from a real PNG file on disk, writes the output PDF, and visually confirm the header shows the logo without disturbing the Bill To/Ship To/line table below it. Delete the throwaway file afterward — it is not part of the plan's committed test suite.

- [ ] **Step 5: Run `module-drift-checker` and `tenancy-security-reviewer`**

These are read-only review agents already available in this repo (see `.claude/agents/`). Dispatch both against the accumulated diff (`git diff master...HEAD` or equivalent) since this touches `controllers/*.go` and a new tenant-scoped endpoint. Triage findings: fix CRITICAL/HIGH inline, report MEDIUM without fixing.

- [ ] **Step 6: Final report**

Summarize files changed, test results, and any unresolved findings.
