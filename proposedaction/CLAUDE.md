# ProposedAction — Claude rules

_Loaded automatically when Claude reads files under `proposedaction/`. Moved verbatim from the root CLAUDE.md._

## ProposedAction (`proposedaction/` + `/api/tenant/actions`)
Propose-then-confirm write path for the future AI agent and document auto-create. **The LLM never holds
credentials or calls write handlers**: it can only create a `ai_proposed_action` row the user sees in full.
1. **Executors are Go-defined** (`proposedaction.Registry`, keyed by resource + kind; none registered in `main.go` yet,
   `NoopExecutor` is for tests). `Execute(ctx, tx, caller, payload)` must call the module's EXISTING store path, never raw SQL.
2. **No HTTP create route.** `Store.Propose` is called from Go (agent / docextract code) and validates + previews the payload.
3. **Routes** (`tenantChain`): `GET /api/tenant/actions` (caller's own pending, keyset), `GET .../{id}`, `POST .../{id}/confirm`,
   `POST .../{id}/cancel`. Reads are own-rows-only by construction (`identity_id` predicate).
4. **Confirm is security-critical** (`store_confirm.go`): row lock; other identity or missing => **404** (+ `proposed_action_idor`
   security event on mismatch); `authz.Check(target resource, create|update)` + executor `InScope` re-run under CURRENT roles
   (missing resource permission => 403 + `permission_denied`; row-level scope denial => **404** identical to an unknown id
   + `proposed_action_idor`, so ids can't be enumerated; either way the row stays pending and nothing executes); payload re-validated; atomic
   `UPDATE ... WHERE status='pending' AND expires_at>now()`; execute and flip share one tx; executor error => rollback + row marked
   `failed` with a sanitized message (422). Replay of a confirmed action => 200 with stored `result_ref`, no re-execute.
   **Expired => 410**; cancelled/failed => 409; invalid or failed execution => 422 (410/422 extend the usual status set on purpose). Never log or return payloads.
5. **Audit + events.** Execute runs under `provenance.AIAgent|DocExtract` (recorded as `via` in audit details) and emits
   `proposed_action.confirmed` in the same tx. Expired rows are swept lazily on read and by the `proposed_action_sweep` job
   (TTL `PROPOSED_ACTION_TTL_MINUTES` 15, resolved-row retention `PROPOSED_ACTION_RETENTION_DAYS` 7).
