# Document extraction — proposed new records (F1 part 1) — design

First real consumer of the Foundations `ProposedAction` path
(`2026-10-22-domain-event-proposed-action-design.md`). When extraction finds a customer, vendor or
item that matches nothing, the review screen offers "Create vendor 'Acme Stone'?"; a user click
confirms it through `POST /api/tenant/actions/{id}/confirm`. Nothing is created without a click.

## Verified facts (code, not brief)
- Resolution lives in `docextractjob.ResolveDoc` (called by the pipeline AND by `Refresh` on every
  `GET /document-extractions/{id}`), stored in `document_extractions.result` JSONB (`ResultDoc`:
  `Extracted`, `Resolution`, `Duplicates`). Unmatched party: `Resolution.Customer|Vendor` with empty
  `UUID`, `ConfNotFound`, optional `Candidates`. Unmatched line: `LineMatch.Item == nil` + flag
  `item_unmatched` (`item_ambiguous` = several rows share the key). Proposal data for a line is in
  `Extracted.Lines[m.Index]` (SKU, Description, UoM, UnitPriceCents, QtyMilli).
- `/complete` (controllers/document_extraction_complete.go) only needs the saved record uuids; alias
  learning works for a newly created party/item once the form reselects it.
- `proposedaction.Executor.Execute(ctx, tx, caller, payload)` runs inside the confirm tx; the registry in
  `main.go` is empty. `ai_proposed_action.source` already allows `docextract`; no `resource` CHECK.
  Replay returns the stored result, cross-identity is 404, expired is 410. Executor 422 text is fixed
  ("The action could not be completed."), payload values never echoed.
- **Existing creates cannot run inside the confirm tx**: `vendors.Create`, `inventory.Create` open their
  own `pool.Begin` and read back on the pool; `crmstore` customer create is entirely pool-bound.
  All three bodies already use only Query/QueryRow/Exec (`workflow.Querier`), so a tx variant is a
  mechanical extraction; the old functions become thin wrappers (behaviour unchanged).
- Customer create requires `customer_contact_email` (portal invite). Extraction never finds an email.
- Item create requires sku, name, unitId, unitPrice, purchaseUnitPrice (`validateItemInput`).
- Extraction carries **no tax ID, phone, email, or structured address** for any party. Only free-text
  `BillTo/ShipTo` and a letterhead block (`header_party.go extractLetterhead`).
- **No masking** of bank/ACH/card numbers exists anywhere (docextract, docextractjob, llm adapter), and no
  length cap on extracted values. Only PDF junk-rune filtering.
- The inventory item HTTP handler does not call `LogAuditFull`; customer and vendor do.
- Welcome email (`notifyCustomerWelcome`), item `NotifyActorCreated` and the RAG enqueue are post-commit
  side effects of the HTTP handlers; `proposedaction` has no post-commit hook.

## Decisions
1. **Derive at resolve time, propose on click.** `DeriveProposedEntities(extracted, resolution)` is a pure
   function called from `ResolveDoc`, so it runs in the pipeline and on every `Refresh`; output is stored in
   `ResultDoc.ProposedEntities` (and compared in `sameResolution` so it persists). `ProposedAction` rows are
   created **only when the user asks** (`POST …/proposed-entities/{key}/propose`) — no rows for documents
   never reviewed, no TTL race with a human reviewing hours later. Expired/cancelled proposal ⇒ click
   again ⇒ a new row from the stored entity; no re-extraction. A pending/confirmed row for the same
   (extraction, key) is returned instead of creating a duplicate (idempotent propose).
2. **Linkage without a migration.** Payload carries `sourceRef = "<extractionId>:<entityKey>"`;
   `proposedaction` gains `ListBySourceRef(identityID, source, extractionID)` (reads
   `payload->>'sourceRef'` under the existing `(identity_id, status)` index). No schema change.
3. **Entity shape** `ProposedEntity{Key, Kind(customer|vendor|item), Name, Fields, Confidence,
   Source{Field,Page,Row,Lines[]}, UnresolvedReason, Needs[]}`. Key = `pe_` + sha256(kind|normalized key)[:12].
   Derive rules: customer only for `sales_order` with a found, non-own-company name, `UUID==""`, no
   candidates (ambiguous ⇒ reviewer picks an existing one); vendor likewise for PO/bill; item only for
   product/addon lines with `item_unmatched`; inactive matches never proposed. De-dup within a document by
   `duplicate.Key(name)` / normalized SKU (+ description key), collecting all line indexes.
   `Needs` lists user inputs required before a proposal can be created: `contactEmail` (customer),
   `sku` (item without a document SKU).
4. **Overrides, whitelisted.** Propose body `{overrides:{contactEmail?, sku?}}`; anything else ignored.
   Resource, kind, permission and owner come from the entity kind and the JWT — never from document text.
5. **Item defaults** (document numbers only, never LLM): SO ⇒ unitPrice = line price, purchaseUnitPrice 0;
   PO/bill ⇒ purchaseUnitPrice = line price, unitPrice 0. Unit: doc UoM normalized via
   `docextract.NormalizeUnit` matched to `lkp_unit.unit_code`; unmapped ⇒ `EA`, shown in the preview
   ("document said 'xyz'"). Tracking quantity, trackStock default. The 3B LLM is not involved.
6. **Executors** live in a new package `proposedentity` (imports vendors/inventory/crmstore/workflow/
   authz/proposedaction; `docextractjob` stays free of them): `docextract.create_customer|vendor|item` are
   Kind `create` on resources `customer|vendor|inventory_item` (registry key is resource+kind, so the
   "names" are labels in previews/logs). Each: `Validate` (strict decode, DisallowUnknownFields, length/
   control-char/email checks, no secrets-shaped values), `Preview`, `InScope=true` (create has no scope;
   `authz.Check(create)` is done by the store), `Execute` → new `CreateTx` helper under `provenance`
   DocExtract, resolving the actor employee from `caller.UserID`, writing `LogAuditFull(tx, …)` (via
   stamped automatically). Customer V1-design tenants are rejected in Validate/Execute.
7. **Tx-aware create refactors** (behaviour-preserving, existing tests must stay green): `vendors.CreateTx`,
   `inventory.CreateTx`, `crmstore` `CreateCustomerTx` (helpers retyped `*pgxpool.Pool → workflow.Querier`;
   readback via tx). Old entry points delegate.
8. **Gaps accepted & flagged**: no welcome email / item-created bell / immediate RAG enqueue from the
   executor (no post-commit hook; the RAG 10-minute reconcile and audit observer cover indexing); item
   create now writes an audit row via=docextract (the UI path writes none) — needed for SOC 2 provenance.
   Customer address is shown to the reviewer but **not persisted** (free text cannot be mapped to structured
   address fields reliably); vendor `physicalAddress` is persisted when a block was found. Tax ID is
   supported in the payload/preview contract but extraction does not produce one today.
9. **Sanitization & masking** (`docextract/sanitize.go`, leaf): `Clean(s, max)` strips control/format/bidi
   runes, collapses whitespace, clips by runes. `MaskSensitive(s)`: Luhn-valid 13–19 digit runs, IBANs, and
   digit runs following ACH/routing/ABA/account labels become `[masked]`. Applied (a) to the residual text
   before the LLM prompt, (b) to stored free-text fields (Snippets, Notes, BillTo, ShipTo, line
   Detail/Description snippets, page-row words) after parsing, so parsed numbers are unaffected; (c) to every
   proposed-entity field. Extracted text is never RAG-indexed by this feature.
10. **Gating.** New env `DOC_EXTRACT_PROPOSE_ENTITIES_ENABLED` (default **false**) on top of the existing
    `DocExtractEnabled` + tenant AI toggle. Off ⇒ `proposedEntities` omitted from GET, propose endpoint 404.
    Executors are registered regardless (inert without proposals).
11. **Security**: propose is owner-only (existing `authOwned`; other owner ⇒ 404 + `doc_extract_idor`),
    requires doc-type `create` AND the target resource `create` (denied ⇒ 403 + `logSecurityEvent
    permission_denied`; the GET view carries `canCreate:false` + `reason` per entity so the chip disables).
    Confirm re-authorizes under current roles (Foundations). Body capped, JSON strict.

## API
- `GET /document-extractions/{id}` → `result.proposedEntities[]`, each `{…entity, canCreate, reason?,
  action?{id,status,expiresAt,preview,resultRef,errorMessage}}` (latest proposal for that key).
- `POST /document-extractions/{id}/proposed-entities/{key}/propose` `{overrides?}` → `{success, action}`
  (201 new / 200 existing). 404 unknown key/extraction/other owner; 403 no create permission; 409 not
  ready or already used; 422 `needs` missing/invalid.
- Confirm/cancel: existing `/api/tenant/actions/{id}/…`.

## Frontend (StoneSuite-WebUI)
Chips in the review fields panel (customer/vendor, beside `PartyChoices`) and on unmatched lines
(`DocumentLinesPanel`). A chip: name, confidence, source snippet link, "Review & create" → inline panel with
the mounted `ProposedActionCard` (extended: `disabledReason`, `onConfirmed(result)`, link to created record,
`replayed`). Missing-input fields (email/SKU) before propose. States: needs-input, ready (not proposed),
proposing, pending, confirming, created (calls `review.pickParty` / sets the line item, invalidates
`['document-extraction', id]`), failed ("may already exist" → refetch), expired (re-propose), forbidden
(disabled + reason), unavailable (404). `RESOURCE_QUERY_KEYS` registered for customer/vendor/inventory_item.

## Testing
Table-driven: derive, sanitize/mask, payload validation. Per executor: happy, no permission (403), cross
identity (404), replay, expired (410), tenant-isolation (two tenant DBs), rollback atomicity (failed create
leaves no row and marks failed). Regression: resolver/alias/duplicate suites untouched + new derive tests
asserting resolution output is unchanged. Frontend: every chip state + forbidden.

## Out of scope
OCR, silent create, worker process, comms/WhatsApp, anomaly, auto-PO, agent tools, master.
