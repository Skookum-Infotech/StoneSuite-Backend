# Docextract proposed records — implementation plan

Spec: `docs/superpowers/specs/2026-10-22-docextract-proposed-records-design.md`.
Branches: backend + WebUI `feat/docextract-proposed-records`. Implementers: `go-implementer` (Sonnet) for Go,
Sonnet subagents for WebUI. Each step names files it may touch. No commits until user approval.

## Backend
| # | Step | Mode | Files |
|---|---|---|---|
| B1 | `docextract/sanitize.go`: `Clean`, `MaskSensitive` (Luhn card, IBAN, labelled routing/account) + table tests; apply mask to LLM residual in `llmfallback.go BuildPromptFor` and to stored free-text fields in the pipeline after extract (Snippet, Notes, BillTo, ShipTo, Detail, page-row words — never numeric-parsed values); regression test that existing fixtures' parsed numbers/SKUs/PO numbers are unchanged | independent | docextract/sanitize*.go, llmfallback.go, docextractjob/pipeline.go (+tests) |
| B2 | `vendors.CreateTx`, `inventory.CreateTx` (+ `validateCustom` → Querier); old funcs delegate; existing tests green | independent | vendors/store.go, inventory/store_create.go, inventory/store.go |
| B3 | `crmstore`: `CreateCustomerTx(ctx, q workflow.Querier, in)` returning the record read via q; retype create-path helpers to `workflow.Querier`; V1 store unchanged; existing `CreateRecord` delegates | independent | crmstore/relational_*.go, crmstore store iface file (additive) |
| B4 | config: `DocExtractProposeEntities` ← `DOC_EXTRACT_PROPOSE_ENTITIES_ENABLED` (default false) + `DocExtractSettings` field | independent | config/config.go, controllers/document_extraction.go (settings only) |
| B5 | `docextractjob/proposed.go`: types + pure `DeriveProposedEntities`; add `ResultDoc.ProposedEntities`; call from `ResolveDoc`; include in `sameResolution`; table tests (derive rules, de-dup, own company, ambiguous, inactive, needs) + dbtest proving resolution/alias/duplicate output unchanged | after B1 | docextractjob/proposed*.go, refresh.go, resolve.go |
| B6 | `proposedaction.ListBySourceRef` (+ unit/dbtest) | independent | proposedaction/store_read.go |
| B7 | package `proposedentity`: payload structs, strict decode/validate/sanitize, previews, 3 executors (customer/vendor/item), unit lookup, `Register(reg)`; tests incl. dbtest: happy, no permission, cross-identity 404, replay, expired, rollback atomicity, two-tenant | after B2,B3,B6 | proposedentity/*.go |
| B8 | controllers: `ProposeEntity` handler + route + GET view enrichment (`canCreate`, `reason`, `action`), security events, flag gating, strict body; `main.go` registers executors (before `NewProposedActionOps`) and the route; regenerate apidocs | after B4,B5,B7 | controllers/document_extraction_entities*.go, document_extraction_read.go, main.go, apidocs/ |
| B9 | Docs: help doc in `docs/` (+ embed manifest), `CLAUDE.md` "Docextract proposed entities contract" section | after B8 | docs/…, CLAUDE.md |

## Frontend (after B8 contract is fixed)
| # | Step | Files |
|---|---|---|
| F1 | types + service `proposeEntity`, `RESOURCE_QUERY_KEYS` (customer→crm-records/customer+customer-picker, vendor→vendors+vendor-picker, inventory_item→inventory-items+inventory-item-picker) | types/documentExtraction.ts, services/documentExtractionService.ts, hooks/useProposedActions.ts |
| F2 | extend `ProposedActionCard` (disabledReason, onConfirmed result, created-record link, replayed) + tests | components/proposedAction/* |
| F3 | `ProposedEntityChip` + hook (needs-input, propose, states) and mount in `ReviewFieldsPanel` and `DocumentLinesPanel`; on created → `pickParty`/line item + invalidate extraction | components/tenant/documentReview/*, hooks/* |
| F4 | tests for every state incl. forbidden/404/410/failed; lint, `npx tsc -b`, test, build, `npm audit --omit=dev --audit-level=high` | *.test.tsx |

## Verify / review (Phase 3-5)
`go-verifier` on the whole diff (build, vet, test, `-tags dbtest` with fresh pgvector DB per package, `-race` on
touched pkgs, golangci-lint). Haiku checkers: `tenancy-security-reviewer` (controllers, proposedentity),
`module-drift-checker` (vendors, inventory, controllers, main.go), `migration-auditor` N/A (no schema change),
`filter-invariant-checker` N/A. Fix loop capped at 2 rounds, HIGH/CRITICAL only. Browser verification of the
review flow if a dev stack can run; otherwise reported as not exercised.
