# Dashboard Widget Grant Filtering — Design

**Status:** proposed

## Problem

Reported symptom: a user switches their active role (e.g. to a purchasing-focused
role) and the dashboard shows "Couldn't load sales orders snapshot" / "Couldn't
load top customers" cards after a hard reload — not stale data, a real failure.

Root cause: **which widgets a role's dashboard shows** (`role_dashboard_widgets`,
admin-configured via `PUT /api/tenant/dashboard/widgets/roles`) is entirely
decoupled from **whether that role can actually read the data each widget
needs**. `dashboardui.GetForIdentity` ([dashboardui/store.go:96](../../../dashboardui/store.go))
resolves the caller's visible widget ids purely from the active role's saved
allocation (or the catalog default), with no check against the caller's RBAC
grants for the underlying resource. Several widget data endpoints are
**single-gated** — they 403 outright if the caller lacks one specific
`resource:read` grant (verified in `controllers/dashboard_*.go`):

| Widget id | Required grant |
|---|---|
| `sales-orders-snapshot` | `sales_order:read` |
| `top-customers` | `invoice:read` |
| `inventory-alerts` | `inventory_item:read` |
| `material-consumption` | `inventory_item:read` |
| `purchases-status` | `purchase_order:read` |
| `ar-outstanding` | `invoice:read` |
| `accounting-snapshot` | `cash_transfer:read` |

If an admin allocates one of these widgets to a role that doesn't hold the
matching grant (or the role's grants change after the allocation was made),
every member of that role gets a guaranteed-broken card, every time — this is
what the user hit.

Three widgets are **composable/degrading, not single-gated**, and are
correctly out of scope for this fix: `kpi-strip` and `pipeline-donut` already
build their payload from several independently-checked sub-metrics/stages and
only omit the ones the caller can't see (never showing a top-level fake
zero); `recent-records` already treats each source module as independently
grantable, same pattern. These only fail widget-wide when *none* of their
sources are granted, which is already a deliberate, meaningful 403 ("you can
read literally nothing this widget draws from") — not the bug being fixed.

## Fix

`GetForIdentity` already loads the caller's `EffectiveGrants` (correctly
narrowed to the active role — this part of the RBAC/active-role wiring was
independently verified correct end-to-end and needs no change). Filter the
allocated widget id list against those same grants before returning it: a
single-gated widget id is dropped from the visible set when the caller's
grants don't include its required resource's read action. Composable widgets
are never filtered by this check — the existing per-source degrade logic in
their own endpoints already handles partial access correctly.

This mirrors the codebase's established convention (buildPipelineMix,
buildRecentRecords, buildNeedsApproval): a source/widget the caller can't
read is **omitted**, never shown broken.

## Scope

- `dashboardui/store.go`: add a widget-id → required-resource lookup for the
  seven single-gated widgets; filter `GetForIdentity`'s result against the
  already-loaded `grants` using `authz.DecideAny` (existing exported helper,
  no new authz surface, no extra DB round trip).
- `dashboardui/store_test.go`: table-driven coverage — a single-gated widget
  is dropped when the grant is missing and kept when present; a composable
  widget (`kpi-strip`) is never dropped by this filter regardless of grants;
  the super-admin wildcard fast path is unaffected (already returns before
  this filter would run).

No route, schema, or `query/` change. No change to `SetForRoles`/the
allocation-write path — an admin may still allocate a widget to a role ahead
of granting the matching permission (e.g. while building out a new role); the
fix is that the caller's own dashboard never shows the resulting broken card
in the meantime.
