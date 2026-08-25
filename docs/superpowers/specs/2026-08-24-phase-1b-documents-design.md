# Phase 1b — Documents (PDF · Attachments · R2 · Email · Send Flow) — Backend Design Spec

**Date:** 2026-08-24 · **Status:** Implemented — see §10 for two decisions revised during implementation
**Author:** Backend architecture pass (Claude)
**Scope:** `quote`, `estimate`, `salesorder`, `invoice` (the "core four" customer-facing sales documents).

---

## 1. Problem

Phase 1b calls for **documents**: generate a customer-facing PDF of a quote / estimate /
sales order / invoice, and let a staff user email it to the customer with the PDF
attached, keeping a record of what was sent.

Much of the raw infrastructure already exists and must **not** be rebuilt:

- **R2 storage** — full S3-compatible client with SigV4 presigning (`storage/r2.go`).
- **Attachments** — complete presigned upload → confirm → list → download → delete
  flow, generic over any record via `record_id`, with RBAC/IDOR gating and audit
  logging (`controllers/attachments.go`, `workflow/attachments.go`).
- **Generic record resolution** — `workflow.ResolveRecordAccess` already resolves all
  four modules by UUID, returning workflow key + owner user id
  (`workflow/attachments.go:120`).
- **Email** — Resend / SMTP / no-op fallback (`services/email.go`).

The genuinely new work is: (1) **PDF generation** — there is no PDF library in `go.mod`
and the build is `CGO_ENABLED=0` on Alpine; (2) **document email** — the current email
layer sends HTML bodies only, with **no attachment support**; (3) the **send flow** that
ties render → store → email → track together.

## 2. Decisions (locked)

| # | Decision | Choice |
|---|---|---|
| D1 | Where PDFs are generated | **Backend, pure-Go** — server-authoritative, needed for email + portal + audit consistency; stays within `CGO_ENABLED=0`. |
| D2 | PDF library | **`go-pdf/fpdf`** — one pure-Go dependency, no transitive bloat; matches this repo's stdlib-first, minimal-dependency ethos. |
| D3 | Document scope this phase | **Core four:** `quote`, `estimate`, `salesorder`, `invoice`. Receipts (payment / credit memo / refund) are a later phase. |
| D4 | What happens to the sent PDF | **Persist + attach + track** — store the exact PDF via the existing attachment layer, attach it to the email, and record send metadata. |
| D5 | Recipient model | **Doc contact, editable + CC** — pre-fill the customer contact email from the document, let the sender edit/add recipients, optional CC to the sender. |
| D6 | Endpoint shape | **Generic, record-keyed** (like attachments) — not cloned per module. Only a small mapping adapter is repeated per module. |
| D7 | Send tracking storage | **One generic `document_sends` history table** keyed by `record_id` — not per-module "last sent" columns. Gives full history and avoids editing four `CREATE TABLE` bodies. |

## 3. Guiding principle

**Reuse the attachments pattern.** The new layer is thin, generic, and record-keyed.
Everything security-sensitive (RBAC check, IDOR scope guard, 404-on-denial, security
logging) reuses the exact gate already proven in `controllers/attachments.go`. The only
cloned surface is one pure mapping function per module.

## 4. Architecture

### 4.1 `docpdf` package — pure renderer (app-dependency-free)

Same discipline as `query/` and `ai/`: imports nothing app-specific, so it stays
trivially testable and reusable.

- `PrintableDoc` — a normalized, module-agnostic struct:
  - `Seller` (name, address lines, phone, email) — letterhead.
  - `Kind` label (`"INVOICE"`, `"QUOTE"`, `"ESTIMATE"`, `"SALES ORDER"`).
  - `Number`, `Status`, issue date, optional due date.
  - `BillTo`, `ShipTo` address blocks.
  - `Lines []PrintLine` (sku, name, description, qty, unit, unit price, discount %,
    tax %, line total).
  - Money totals: subtotal, discount, tax, shipping, adjustment, grand total, and
    optional amount-paid / balance-due (zero-omitted).
  - `CurrencySymbol`, `Terms`, `Notes`, `Memo`.
- `Render(PrintableDoc) ([]byte, error)` — builds the PDF with `go-pdf/fpdf`:
  letterhead header, doc-meta box, bill/ship columns, a **paginated** line-item table
  (wrapping long descriptions, repeating the header row across pages), totals block,
  terms/notes footer, and page numbers.
- File split to respect the 300-line cap: `docpdf/document.go` (types + `Render`
  entry point + validation) and `docpdf/layout.go` (header / table / totals drawing
  helpers).

### 4.2 Per-module adapter — the only cloned piece

Each of `invoice/`, `quote/`, `estimate/`, `salesorder/` gets a `printable.go`:

- `ToPrintable(doc <T>, seller docpdf.Seller) docpdf.PrintableDoc` — a **pure** mapping
  from the module's typed struct to `PrintableDoc`. No layout code is cloned.
- `Recipient(doc <T>) (email, name string)` — resolves the default recipient from the
  document's billing contact, falling back to the customer name.

Both are pure and table-tested.

### 4.3 Plumbing gaps to fill

- **`storage.Client.Put(ctx, key, contentType string, body []byte) error`** — new
  server-side SigV4 **auth-header** PUT, mirroring the existing `signedDelete` in
  `storage/r2.go`. Needed to upload server-generated PDF bytes (the current client only
  presigns and deletes; browser uploads never touch the server). Returns
  `ErrStorageNotConfigured` on a nil client, same as the other methods.
- **`services` email attachments** — add:
  - `type EmailAttachment struct { FileName, ContentType string; Content []byte }`
  - `SendDocumentEmail(to []string, cc []string, subject, htmlBody string, attachments []EmailAttachment) error`
  - Resend path: add an `attachments` array with base64 `content`.
  - SMTP path: assemble a `multipart/mixed` MIME message (HTML part + base64-encoded
    attachment parts).
  - No-op path unchanged (logs and returns nil when no provider configured).
- **Seller letterhead** — a `controllers` helper `sellerFromTenant(*tenancy.Tenant)
  docpdf.Seller` built from `tenant.DisplayName` plus the company profile parsed out of
  `tenant.Metadata` (the onboarding submission JSON: address, phone, email). **Logo is
  out of scope** — no logo store exists yet; a `Seller.LogoPNG []byte` field is left in
  the struct as a no-op extension point.

### 4.4 Generic controller — `controllers/documents.go`

A new `DocumentOps` handler group, keyed off `record_id`, reusing the `attachAuth`
gate (resolve type via `ResolveRecordAccess` → `resourceForKey` → `authz.Check` →
`recordInScope`; **404** on scope denial; `logSecurityEvent("idor_denied", …)` on IDOR).

Endpoints (all behind `RequireAuth` + TenantResolver):

| Method | Path | RBAC | Behaviour |
|---|---|---|---|
| GET | `/api/tenant/records/{id}/document/pdf` | `<type>:read` + scope | Render on the fly, stream `application/pdf`. No persistence. Preview / download. |
| POST | `/api/tenant/records/{id}/document/send` | `<type>:update` + scope | Render → `storage.Put` to R2 → `workflow.InsertAttachment` (so it appears in the record's attachments and the customer portal) → `SendDocumentEmail` with the PDF attached + CC → insert `document_sends` row → audit `document.sent`. |
| GET | `/api/tenant/records/{id}/document/sends` | `<type>:read` + scope | List send history for the record. |

- **Send request body:** `{ to: []string, cc: []string, subject?: string, message?: string }`.
  When `to` is empty, default to the document's resolved recipient; `subject`/`message`
  default to templated values. Recipients are validated (non-empty, syntactically valid
  email) server-side.
- **R2 required for send:** if R2 is not configured or the tenant has no bucket, `send`
  returns **503** (consistent with attachments) — we persist before emailing, so we do
  not send an un-tracked copy.
- **Document-loader registry:** `map[workflowKey]DocumentLoader`, where
  `DocumentLoader func(ctx, pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error)`
  and `DocMeta` carries the default recipient/subject and the storage-key inputs
  (workflow key, document number). Populated at wiring time in `main.go` so the
  controller does not import the four module packages directly (avoids import cycles and
  keeps it open for future modules). Registry miss → 404 (a record type without a
  registered loader has no printable document).

### 4.5 Send tracking — `document_sends` table

Added to `database/migrations/tenant/schema.sql` with `CREATE TABLE IF NOT EXISTS`
(idempotent, per the migration rules — no editing of existing `CREATE TABLE` bodies):

```
document_sends(
  id             uuid primary key default gen_random_uuid(),
  record_id      uuid not null,
  workflow_key   text not null,
  attachment_id  uuid null,            -- the persisted PDF's attachment row
  sent_to        text not null,        -- comma-joined recipients
  cc             text not null default '',
  subject        text not null default '',
  sent_by_user_id integer null,        -- tenant users.id
  sent_at        timestamptz not null default now()
)
```

Indexed on `(record_id, sent_at desc)` for the history read. This is generic (one table
serves all document types) and records full history rather than only "last sent".

## 5. Security & data-residency notes

- **Every mutation/read reuses the existing gate** — no bespoke authz. Send is a
  mutation (`ActionUpdate`); PDF and history are reads (`ActionRead`). Denial is 404,
  never 403, so ids cannot be enumerated. IDOR attempts hit `logSecurityEvent`.
- **PDFs are server-authoritative** and rendered in-process — no tenant financial data
  leaves for a third-party render service, consistent with the self-hosted-Ollama
  data-residency stance.
- **Emailing external customers** is a core, staff-initiated product action on an
  authenticated request (not agent-autonomous), so it executes on the request without an
  extra confirmation gate; recipients are validated and the send is audit-logged.
- **PDF attachments** are stored under the tenant's own R2 bucket via the existing
  per-tenant storage-key convention (`workflow.GenerateStorageKey`), so they inherit the
  same isolation as user-uploaded attachments.

## 6. Testing

- `docpdf`: table tests — `Render` returns non-empty bytes beginning with `%PDF-`;
  handles empty line list, a line count that forces multiple pages, and missing optional
  fields (no due date, zero paid/balance); `PrintableDoc` validation rejects an empty
  `Kind`/`Number`.
- Per-module `ToPrintable` / `Recipient`: table tests mapping the typed struct →
  `PrintableDoc`, including nil pointers and empty address blocks.
- `storage.Put`: request-construction test (canonical request / signed headers), aligned
  with the existing SigV4 tests; `nil` client returns `ErrStorageNotConfigured`.
- `services.SendDocumentEmail`: MIME-assembly test (multipart boundaries, base64
  attachment part) and Resend-payload marshal test.
- `controllers/documents.go`: httptest coverage for 401 (no auth), 403/404 (scope
  denial), 503 (R2 absent), registry-miss 404, and a happy-path `send` with injected
  fake renderer / storage / email verifying the attachment row, `document_sends` row,
  and audit call. Follows the pattern in `controllers/invoice_test.go`.

## 7. Post-implementation checks (CLAUDE.md discipline)

- `migration-auditor` — after the `document_sends` schema change.
- `tenancy-security-reviewer` — on the new `controllers/documents.go`.
- `module-drift-checker` — after adding the per-module `printable.go` adapters.

## 8. Explicitly out of scope (YAGNI)

- Tenant logo / rich branding store (only `DisplayName` + `Metadata` today).
- Receipts (payment / credit memo / refund) PDFs.
- Client-side PDF generation, headless-Chrome rendering, external PDF APIs (all
  rejected in D1/D2).
- Scheduled / bulk sends, email open tracking, delivery webhooks.
- Editing/versioning a sent PDF — each send persists a new immutable copy.

## 9. Net new surface

1 shared renderer package (`docpdf`), 4 tiny per-module mapping adapters, 1 new R2
method (`Put`), 1 new email function (`SendDocumentEmail` + `EmailAttachment`), 1 generic
controller (`DocumentOps`) with 3 endpoints, 1 generic table (`document_sends`), and the
`go-pdf/fpdf` dependency. Everything security-sensitive reuses existing, reviewed gates.

## 10. Decisions revised during implementation

Two things changed after this spec was approved and while Task 7 was in progress. Both
were explicit product decisions, made when implementation surfaced facts the design
phase didn't have.

**D4 reversed — Send no longer persists to R2.** §2's D4 ("Persist + attach + track")
and §4.4's `Send` description are **superseded**: `Send` now renders the PDF in Go
(`docpdf.Render`) and emails it directly from the in-memory bytes — there is no
`storage.Put`, no `workflow.InsertAttachment`, and no re-downloadable copy afterward.
`document_sends` still records who/what/when was sent (recipients, subject, timestamp,
actor), but its `attachment_id` column is now permanently unpopulated (nullable, so this
is harmless) — a send is tracked as an event, not as a stored file. `storage.Client.Put`
(Task 2) still exists as a general-purpose capability on the storage client; `DocumentOps`
simply no longer calls it. §4.3's R2-required-503 behavior for `Send` no longer applies
(there is nothing left to persist); `GetPDF` was always render-only and is unaffected.

**New dependency — `workflow.ResolveRecordAccess` needed a resolver gap closed.** §1's
claim that `ResolveRecordAccess` "already resolves all four modules" was true only on
the sibling branch `feat/document-status-rejection` (commit `2181526`), not on `master`
— the base this feature actually branched from only resolved `sales_order` among the
four. The missing `quote`/`estimate`/`invoice` branches were ported verbatim (same
`employee → users.id` ownership-resolution pattern already used for
`sales_order`/`cash_transfer`/`vendor_bill`/`expense` in that function) directly onto
this branch, rather than rebasing onto or waiting for the other branch. Both branches now
carry the same lines, so merging `feat/document-status-rejection` into `master` later
should be a clean, redundant no-op for this specific function.
