# Dashboard Widget Grant Filtering Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the dashboard from showing an allocated widget the caller's
active role can't actually read data for (currently renders as a permanent
"Couldn't load X" card). See
[2026-09-15-dashboard-widget-grant-filter-design.md](../specs/2026-09-15-dashboard-widget-grant-filter-design.md)
for the root cause.

**Repo:** `C:\Users\Lenovo\StoneSuite-Backend`, branch
`feature/dashboard-widget-grant-filter` (new, off current branch
`fix/refresh-preserves-active-role`, which is clean/merged-ready).

**Tech stack:** Go, `testify/assert`, no DB needed for this fix (pure function
over an already-loaded `[]authz.Grant`).

## Task 1: Filter `GetForIdentity` by required-resource grant

**Files:**
- Modify: `dashboardui/store.go`
- Modify: `dashboardui/store_test.go`

**Interfaces:**
- Consumes: `authz.Grant`, `authz.Resource`, `authz.ActionRead`, `authz.DecideAny` (all existing, no new authz surface).
- Produces: `GetForIdentity` return value changes (fewer ids in some cases); no signature change.

- [ ] **Step 1: Add the widget → required-resource table**

  In `dashboardui/store.go` (or `catalog.go` if that reads more natural to
  you — either is fine, keep it next to `widgetIDs`), add:

  ```go
  // widgetRequiredResource maps a single-gated widget id to the one
  // authz.Resource its data endpoint requires read on (see
  // controllers/dashboard_*.go — each of these 403s outright without that
  // grant). A widget id absent from this map is composable/degrading
  // (kpi-strip, pipeline-donut, recent-records) and is never filtered here —
  // their own endpoints already omit ungranted sub-parts instead of failing
  // widget-wide.
  var widgetRequiredResource = map[string]authz.Resource{
      "sales-orders-snapshot": authz.ResourceSalesOrder,
      "top-customers":         authz.ResourceInvoice,
      "inventory-alerts":      authz.ResourceInventoryItem,
      "material-consumption":  authz.ResourceInventoryItem,
      "purchases-status":      authz.ResourcePurchaseOrder,
      "ar-outstanding":        authz.ResourceInvoice,
      "accounting-snapshot":   authz.ResourceCashTransfer,
  }
  ```

  Note: `dashboardui` already imports `stonesuite-backend/authz` — no new
  import needed.

- [ ] **Step 2: Filter the union in `GetForIdentity`**

  In `dashboardui/store.go`, `GetForIdentity` currently ends with:

  ```go
  	allocations, err := GetForRoles(ctx, q, roleIDs)
  	if err != nil {
  		return nil, err
  	}
  	union := map[string]bool{}
  	for _, a := range allocations {
  		for _, id := range a.WidgetIDs {
  			union[id] = true
  		}
  	}
  	out := make([]string, 0, len(union))
  	for id := range union {
  		out = append(out, id)
  	}
  	return out, nil
  ```

  Change the final loop to skip any id whose required resource the caller's
  `grants` (already loaded at the top of this function, before the
  `isWildcard` check) don't cover:

  ```go
  	out := make([]string, 0, len(union))
  	for id := range union {
  		if resource, gated := widgetRequiredResource[id]; gated {
  			if !authz.DecideAny(grants, []authz.Resource{resource}, authz.ActionRead).Allowed {
  				continue
  			}
  		}
  		out = append(out, id)
  	}
  	return out, nil
  ```

  Do not touch the `isWildcard(grants)` early return above this — a
  super-admin-equivalent grant already returns the full catalog before
  reaching this filter, correctly, and must keep doing so unfiltered.

- [ ] **Step 3: Add table-driven tests**

  In `dashboardui/store_test.go`, find the existing `GetForIdentity` test(s)
  (there should be at least one exercising the wildcard/default-role paths —
  match its fake `Querier`/grant-seeding style exactly, don't invent a new
  test harness). Add cases:

  1. Role allocated `["sales-orders-snapshot", "kpi-strip"]`, grants contain
     no `sales_order` grant at all → `GetForIdentity` returns only
     `["kpi-strip"]`.
  2. Same allocation, grants include `{Resource: sales_order, Action: read,
     Scope: own}` (note: `own` is enough — this filter only cares about
     *read access existing*, not which scope; the widget's own handler is
     what applies the scope) → returns both ids.
  3. Role allocated only composable ids (`["kpi-strip", "pipeline-donut",
     "recent-records"]`) with zero grants at all → all three still returned
     (composable widgets are never filtered by this check; each one's own
     endpoint decides its own partial-degrade behavior independently).
  4. Wildcard grant (`{Resource: ResourceAny, Action: ActionAny}`) with a
     role allocation that would otherwise be filtered → still returns the
     full `WidgetIDs()` catalog unfiltered (confirms Step 2 didn't disturb
     the existing early return).

- [ ] **Step 4: Verify**

  ```bash
  go build ./...
  go test ./dashboardui/...
  go vet ./...
  ```

  Expected: all pass, no new failures elsewhere (this change has no callers
  outside `dashboardui` and `controllers/dashboardui.go`, which doesn't need
  edits — `Me` already just forwards whatever `GetForIdentity` returns).

- [ ] **Step 5: Commit**

  ```bash
  git add dashboardui/store.go dashboardui/store_test.go
  git commit -m "fix(dashboard): hide widgets the active role can't actually read"
  ```

---

## Out of scope (explicitly, so a fix-loop or reviewer doesn't wander into it)

- Changing `SetForRoles`/the allocation-write path to *validate* at save time
  that a role holds the matching grant before an admin can allocate a widget
  to it. That's a reasonable follow-up UX improvement but a different,
  larger change (admin-facing validation + error messaging) — not needed to
  fix the actual user-facing bug, which is what a *viewer* of that
  role's dashboard sees.
- Any change to the composable widgets' (`kpi-strip`, `pipeline-donut`,
  `recent-records`) own per-source degrade logic — verified already correct.
- Any change to `authz/`, `middleware/auth.go`, or the `switch-role` flow —
  verified already correct; active-role narrowing was the thing that turned
  out NOT to be the bug here.
