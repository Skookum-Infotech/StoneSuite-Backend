# Global Search — Claude rules

_Loaded automatically when Claude reads files under `globalsearch/`. Moved verbatim from the root CLAUDE.md._

## Global Search (`globalsearch/` + `GET /api/tenant/search`)
Cross-module search fans a term out to every registered provider in parallel, each
through that module's own `query.SearchResolver`, gated by its own RBAC resource +
scope; denied/erroring groups are silently omitted. Results are grouped by entity
type and carry `Domain`/`Module` route segments the frontend feeds to
`recordRoute()`.
1. **A module is search-dead until it's registered in `globalsearch/registry.go`**
   (a `providers_*.go` `addProvider(...)`) **and added to
   `globalsearch/registry_test.go`'s `TestRegistry_ExpectedKeys`.** Same
   clone-discipline trap as `approvalchain/registry.go` — the `new-module` skill
   covers this step.
2. **`Provider.Module` is the frontend route segment, not always the registry
   `Key`** (e.g. `fabrication_job` → `sales/installation`, `inventory_item` →
   `inventory/item`). Cross-check every value against the frontend router.
3. **The fan-out runs no SQL of its own** — a provider calls its module's existing
   `Search(...)` with `query.Request{Search: term}`. Don't hand-roll search SQL in
   `globalsearch/`; the exceptions (`userstore.Search`, `crmstore.SearchActivity`)
   still use `query.StripLikeMeta` + `$n` params, never interpolation.
