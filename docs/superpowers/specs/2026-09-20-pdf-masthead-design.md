# PDF Masthead and Layout Polish — Design

**Status:** implemented on `feat/pdf-masthead-polish`
**Supersedes (in part):** [2026-09-18-tenant-pdf-logo-design.md](2026-09-18-tenant-pdf-logo-design.md) — that
spec put the tenant logo in the letterhead and kept the header unchanged for tenants without one; both are
replaced here.

## Problem

Document PDFs (`docpdf.Render`: quote, estimate, sales order, invoice and the vendor-side documents) carried
no StoneSuite branding and looked plain next to the app's own PDF exports (frontend `pdfBranding.ts`) and the
branded emails (#233). Text with accents or dashes also printed as mojibake (`Café` → `CafÃ©`, `—` → `â€"`)
because fpdf's built-in fonts expect Windows-1252 and were being fed UTF-8.

## Design

One renderer, so every document PDF (Export PDF and the Send attachment) gets it. No change to
`PrintableDoc`, the API, RBAC or the schema.

- **Masthead (page 1):** full-bleed ink band (26mm) with a lime rule. Left: the StoneSuite lockup (lime
  mark, light wordmark — an embedded PNG in `docpdf/assets/`, the frontend's `logo-white.png` trimmed to its
  visible bounds), drawn directly on the band. Beside it (10mm gap) the tenant's own logo (`Seller.LogoPNG`),
  also drawn directly on the band — no plate, no divider, matching how the app header shows the two logos
  together. Right: document kind (white) and number (lime), shrunk to fit. An undecodable tenant logo leaves
  the band with the StoneSuite logo alone — a bad upload never fails a render. The StoneSuite logo is
  light-on-dark for this reason, so a test guards that the embedded asset's wordmark is light (near-black
  pixels stay under 5%); a plain white "STONE SUITE" wordmark stands in only if the asset ever fails to
  decode.
  - **Legibility trade-off:** without a plate, a tenant-uploaded logo is only as legible as its own artwork
    — one with dark content and no built-in backdrop could read poorly on the dark band. The default client
    logo below works because its asset has its own black pill baked in. If this becomes a real problem for
    other tenants' uploads, reintroducing a plate (as a fallback only when a logo's own contrast is too low,
    or behind every upload) is the natural follow-up; out of scope here since it was not asked for.
  - **Which tenant logo:** the one uploaded under Company Info. A tenant that has uploaded none gets the
    client logo (Elevation Stone — the one the app header and the frontend's exported PDFs already show,
    `controllers/assets/client-logo.png`), so both logos are always there; `sellerFromTenant` applies it via
    `fallbackLogo`. A tenant whose upload is set but cannot be fetched gets no second logo rather than the
    default, so it never appears under another brand. `PDF_DEFAULT_CLIENT_LOGO=false` turns the default off
    (for when tenants other than that client are served). The Company Info upload screen is frontend PR
    #189, not yet merged, so until it is no tenant has an uploaded logo.
- **Letterhead:** seller name and address at the left; at the right a compact label/value grid (issue date, due
  date; empty values omitted) with the labels kept close to their values, and under it a lime-highlighted
  amount box: **AMOUNT DUE** (the balance) for documents that track payments, **TOTAL** for the rest. The
  dates and the box read as one column: the date rows are inset by the box's padding (`metaEdges`), so the
  three labels start on one vertical line and the three values end on another, and each label is drawn on the
  same baseline as its value. Without a box (the vendor contact card) the rows span margin to margin. The
  status is no longer shown, except on documents that have no amounts at all (the vendor contact card), which
  would otherwise show nothing about their state.
- **Parties:** Bill To / Ship To with a lime accent rule; a party with no details is omitted.
- **Line table:** ink header, zebra rows, hairlines, dashes for zero Disc/Tax. Rows are measured before they
  are drawn, so none straddles a page break; the header repeats on continuation pages.
- **Summary:** totals card (right) with an ink headline block — Balance Due when payments are tracked, else
  Grand Total; terms, notes and memo (left). It moves to a new page as a unit if it won't fit.
- **Spacing:** one `sectionGap` (6mm) below the letterhead, the parties, the line table and the summary.
  It is measured from wherever the last row ends, so the terms sit the same distance under the items for
  one line or a hundred; `sectionTop` is the single place a section break is decided.
- **Payment details** (invoice, quote, estimate and sales order only — `PrintableDoc.ShowPayment`): a
  full-width tinted card under the summary with bank name, account number and wire routing number in three
  columns. Blank fields are left out; with none set the card is omitted. It shares a page with the summary:
  when both don't fit, both move to the next page rather than leave the card alone at the top of it.
  Purchase-side documents never print it — the tenant's own bank account is the wrong party there.
  - **Data:** three `company_profile` columns (`bank_name`, `bank_account_number`, `bank_routing_number`),
    edited under Configuration → Company Info → Payment Details, carried as `Seller.Payment`. Plain text like
    Tax ID, because it prints on every document; Chart of Accounts bank accounts are deliberately not used,
    since that screen masks account numbers.
  - **Update semantics:** `PUT /api/tenant/company-profile` treats an omitted `paymentDetails` as "keep what is
    stored" (a nil pointer), so an older frontend saving the rest of the form cannot erase it; an empty
    object clears it. Responses always carry `paymentDetails`.
- **Footer (every page):** hairline, `KIND NUMBER`, `Page N of M`.
- **Encoding:** `toCP1252` converts all document text once, up front. Characters Windows-1252 cannot represent
  print as `?`; `₹` is spelled `Rs.`.
- **Palette** matches the frontend PDFs and the emails: Tailwind stone neutrals plus the lime `#c2f589`.

## Out of scope / known limits

- Scripts outside Windows-1252 (Devanagari, Kannada, CJK) still can't be drawn with the built-in fonts; that
  needs an embedded Unicode TTF, a separate change.
- The StoneSuite logo source is 256px wide — fine at this size, but a vector or larger master would print
  sharper.
- Tenant logos are drawn as uploaded (Company Info accepts PNG/JPEG only). A logo with a transparent or white
  margin around it renders small, and a large upload bloats every PDF; auto-trimming and downscaling at upload
  time would fix both and is a natural follow-up.
- No per-tenant switch for the StoneSuite masthead. If white-labelling is needed, gate `drawMasthead` on a new
  `PrintableDoc` field.
- The frontend's own jsPDF exports are untouched.

## Verification

Table-driven unit tests for the masthead, font fitting, encoding, totals, money formatting and pagination.
Sample documents (full invoice, sales order without ship-to, long kind/number, four-page invoice) were
rendered and reviewed by eye.
