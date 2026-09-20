# Per-Tenant PDF Logo — Design

**Status:** proposed

## Problem

Generated document PDFs (quote/estimate/salesorder/invoice/payment/creditmemo/
refund/vendors — the shared `docpdf.Render` pipeline) show only a text
letterhead (`docpdf.Seller.Name` + address lines). There is no logo, on any
tenant's document, ever. `Seller.LogoPNG []byte` already exists as a field
([docpdf/document.go:19](../../../docpdf/document.go)) but its own doc comment
says "reserved extension point; no logo store today" — nothing populates it.
`controllers/documents.go`'s `sellerFromTenant` builds the letterhead purely
from the tenant's control-plane `metadata` JSON (legacy onboarding form data);
it has no path to an image at all.

Separately (already fixed in this session, not part of this spec): the Bill
To / Ship To address blocks in `docpdf/layout.go` used fixed-width `Cell`
instead of `MultiCell`, so a long address line overflowed past its column
into the neighboring one. Flagging only so it isn't reworked here.

## Fix

Add per-tenant logo upload, storage, and PDF rendering, reusing existing
infrastructure end to end rather than inventing new plumbing:

- **Storage**: the tenant's own `company_profile` table (Configuration →
  Company Info; [companyprofile/companyprofile.go](../../../companyprofile/companyprofile.go))
  is the correct home — it is *already* the tenant's editable company
  identity, superseding the legacy onboarding-metadata path that
  `sellerFromTenant` currently reads (see `cmd/backfill-company-profile`'s own
  doc comment on why metadata is legacy). Add one column,
  `logo_r2_key VARCHAR(255) NOT NULL DEFAULT ''`, holding an **R2 object key**,
  not bytes. The image itself lives in the tenant's own R2 bucket
  (`tenants.r2_bucket`), the same isolation model record attachments already
  use.

- **Upload/delete endpoints**, tenant-scoped, reusing the existing
  `company_profile` RBAC resource (`configure` for write, `read` for read) —
  no new RBAC catalog entry, same precedent as `controllers/company_location.go`:
  - `PUT /api/tenant/company-profile/logo` — request body is the raw image
    bytes (`Content-Type: image/png` or `image/jpeg`). Rejects anything that
    doesn't decode as a valid PNG or JPEG, and anything over 2MB. On success,
    uploads to the tenant's R2 bucket at a fixed key (`company/logo.png` or
    `company/logo.jpg`, replacing any previous logo file — a tenant has at
    most one logo) and writes that key to `company_profile.logo_r2_key`.
    Returns `503` when R2 isn't configured (`r2Client == nil`), matching
    `AttachmentOps`'s existing convention for that case.
  - `DELETE /api/tenant/company-profile/logo` — clears `logo_r2_key` and
    best-effort deletes the R2 object (mirrors
    `AttachmentOps.DeleteAttachment`'s best-effort delete).
  - `GET /api/tenant/company-profile` (existing handler) additionally returns
    a `logoUrl` (short-TTL presigned GET, same TTL convention as attachment
    downloads) when `logo_r2_key` is set, so a future frontend can preview it.
  - **Format decision**: PNG/JPEG only. `go-pdf/fpdf` (vendored, no
    replacement planned) natively embeds only PNG/JPEG/GIF — not WEBP.
    Accepting WEBP would mean adding `golang.org/x/image/webp` as a new
    dependency in a vendored repo purely to decode a format the PDF engine
    still can't embed directly; not worth it for a single-image settings
    field. Uploaders with a WEBP logo convert it first (a five-second manual
    step) — no different in practice from any other image-dimension/format
    constraint a settings upload might have.

- **Render wiring**: `controllers/documents.go`'s `sellerFromTenant` becomes
  `sellerFromTenant(ctx, pool, r2Client, tenant)` — `pool` (tenant DB) and
  `r2Client` are already in scope at its one call site
  ([documents.go:89](../../../controllers/documents.go)), so this is a
  same-request read, not a new round trip pattern. It loads
  `companyprofile.Get` (switching the letterhead's data source from legacy
  metadata JSON to the structured, user-editable company profile — the
  correct source, and the only one that can carry a logo key) and, when
  `logo_r2_key` is set, fetches the bytes via
  `r2Client.WithBucket(tenant.R2Bucket).Get(ctx, key)` into `Seller.LogoPNG`.
  Any failure fetching the logo (missing key, R2 error, R2 not configured) is
  logged via `slog` and **swallowed** — a broken or absent logo must never
  fail PDF generation, it just falls back to today's text-only header.

- **`docpdf/layout.go` header**: when `LogoPNG` is non-empty, draw it top-left
  capped at 35mm×18mm (aspect ratio preserved — fpdf's image registration
  reports natural dimensions, scale to fit within the box), with the seller
  name/address block shifted right of it by the drawn logo's width plus a
  small gutter. The document `Kind`/number/status block stays top-right,
  unchanged. When `LogoPNG` is empty, the header renders exactly as it does
  today — byte-for-byte no regression for tenants without a logo. This is
  the only visual change; Bill To/Ship To, the line table, totals, and footer
  are untouched, per explicit scope agreement (no broader redesign toward the
  Zoho-style reference layout also discussed).

## Scope

- `database/migrations/tenant/schema.sql`: `ALTER TABLE company_profile ADD
  COLUMN IF NOT EXISTS logo_r2_key VARCHAR(255) NOT NULL DEFAULT '';` —
  idempotent, additive, no down-migration.
- `companyprofile/companyprofile.go`: add `LogoKey` to `Profile`; extend
  `Get` to select it; add a narrow `SetLogoKey(ctx, q, key string) error`
  (upsert-on-id=1, not a full-profile `Upsert`, so setting/clearing the logo
  never touches or requires the rest of the profile).
- `controllers/company_profile.go`: extend `CompanyProfileOps` to hold the R2
  client (constructor signature changes: `NewCompanyProfileOps(r2Client
  *storage.Client)`), add `UploadLogo`/`DeleteLogo` handlers, extend
  `GetProfile`'s response with `logoUrl`.
- `controllers/documents.go`: `sellerFromTenant` gains `pool`/`r2Client`
  params and switches its data source to `companyprofile.Get` + R2 fetch;
  update its one call site.
- `docpdf/document.go` / `docpdf/layout.go`: no field changes to
  `PrintableDoc`/`Seller` (`LogoPNG` already exists) — `drawHeader` gains the
  image-drawing branch described above.
- `main.go`: wire `NewCompanyProfileOps` with the existing `r2Client`
  (already constructed for `AttachmentOps` at this point in `main.go`);
  register the two new routes.
- Tests: table-driven coverage for `companyprofile.SetLogoKey`/`Get` round
  trip, `UploadLogo` validation (rejects non-image, rejects oversize, accepts
  PNG/JPEG), `sellerFromTenant`'s logo-fetch-failure-is-non-fatal path, and a
  `docpdf` render test asserting the header draws taller/wider when a logo is
  present vs. absent (same style as the existing `TestRender` table).

No change to any other document module's own logic, no schema change outside
the one additive column, no RBAC catalog change, no change to the Bill
To/Ship To/line-table/totals/footer layout.
