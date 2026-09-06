package controllers

import (
	"net/http"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
)

// recentRecordsLimit is how many rows the Recent records widget shows. Each
// source is asked for recentRecordsLimit+1 rows (see buildRecentSources)
// rather than exactly recentRecordsLimit -- that one extra row per module is
// what makes mergeRecentRecords' hasMore exact rather than approximate: any
// record belonging in the true global top-(limit+1) has, by definition, at
// most `limit` more-recently-updated records anywhere in the tenant, so it
// has at most `limit` more-recently-updated records within its own module
// too -- meaning it is guaranteed to appear in that module's own
// top-(limit+1). So the merged pool across every granted module's
// top-(limit+1) always contains the true global top-(limit+1), and "pool
// size > limit" after the merge exactly answers "does a (limit+1)th global
// record exist", never a guess bounded by what any single module happened
// to return.
const recentRecordsLimit = 6

// recentRecord is one row in the Recent records widget's response, merged
// across every document module the caller can read. Presentation (badge
// color, relative time, route building) stays client-side -- see
// RecentRecordsTable.tsx -- the backend sends raw values, matching Pipeline
// mix and KPI strip's convention.
type recentRecord struct {
	ID           string    `json:"id"`
	Module       string    `json:"module"` // authz.Resource / workflows.key, e.g. "sales_order" -- the frontend's route table keys off this
	Domain       string    `json:"domain"` // "crm" | "sales" | "purchases" -- for the widget's filter chips
	RecordNumber string    `json:"recordNumber"`
	Account      *string   `json:"account"` // customer/vendor display name; null when the module has neither (e.g. Expense)
	Value        *float64  `json:"value"`   // null when the module carries no single monetary total (CRM stages, Item Receipt)
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// recentSource is one module the Recent records widget can pull from. fetch
// is late-bound to just the caller's resolved scope for this module --
// everything else (ctx, pool, actorIdentityID, since, limit) is already
// closed over by buildRecentSources (see dashboard_recent_sources.go).
type recentSource struct {
	module   string
	domain   string
	resource authz.Resource
	fetch    func(scope authz.Scope) ([]recentRecord, error)
}

// grantedSource pairs a recentSource the caller can read with the RBAC scope
// buildRecentRecords' check resolved for it.
type grantedSource struct {
	source recentSource
	scope  authz.Scope
}

// updatedAtSinceFilter narrows a module's Search to records updated at or
// after since, or no filter at all when since is zero -- the "all" range has
// no lower bound, mirroring parseDashboardRange.
func updatedAtSinceFilter(since time.Time) []query.Clause {
	if since.IsZero() {
		return nil
	}
	return []query.Clause{{Field: "updated_at", Op: query.OpGte, Value: since.Format(time.RFC3339)}}
}

// mergeRecentRecords flattens perSource (one already-limited, module-scoped
// slice per granted source) into a single feed sorted newest-updated-first,
// truncated to limit. hasMore is true when the merged pool holds more than
// limit rows -- see recentRecordsLimit's doc comment for why that's exact,
// not an approximation.
func mergeRecentRecords(perSource [][]recentRecord, limit int) (records []recentRecord, hasMore bool) {
	var pool []recentRecord
	for _, rows := range perSource {
		pool = append(pool, rows...)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].UpdatedAt.After(pool[j].UpdatedAt) })
	if len(pool) > limit {
		return pool[:limit], true
	}
	return pool, false
}

// buildRecentRecords resolves the Recent records widget's data given two
// injectable dependencies -- check (a source's authz decision) and fetchAll
// (every granted source's already-limited rows, one slice per source, in
// the same order as granted) -- so the per-source skip logic and the
// cross-module merge are testable without a real tenant pool or goroutines
// (see dashboard_recent_test.go). The HTTP handler wires check to
// authz.Check and fetchAll to fetchAllRecent, a concurrent fan-out over
// every granted module's own Search.
//
// A source the caller holds no read grant on is skipped entirely, never
// reported as empty -- same convention as buildPipelineMix and
// buildNeedsApproval. ok=false only when none of the sources are granted;
// the handler maps that to 403.
func buildRecentRecords(
	sources []recentSource,
	check func(resource authz.Resource) (authz.Decision, error),
	fetchAll func(granted []grantedSource) ([][]recentRecord, error),
	limit int,
) (records []recentRecord, hasMore bool, ok bool, err error) {
	var granted []grantedSource
	for _, src := range sources {
		decision, err := check(src.resource)
		if err != nil {
			return nil, false, false, err
		}
		if decision.Allowed {
			granted = append(granted, grantedSource{source: src, scope: decision.Scope})
		}
	}
	if len(granted) == 0 {
		return nil, false, false, nil
	}

	perSource, err := fetchAll(granted)
	if err != nil {
		return nil, false, false, err
	}

	records, hasMore = mergeRecentRecords(perSource, limit)
	return records, hasMore, true, nil
}

// fetchAllRecent runs every granted source's fetch concurrently -- each is
// an independent read over the shared, concurrency-safe pgxpool.Pool, so
// there is no reason to pay for up to 17 module round trips in series.
// Module/Domain are stamped here (not by the per-type mapper functions in
// dashboard_recent_sources.go) so those mappers stay concerned only with
// their own type's fields. The first error from any source aborts the rest
// (errgroup semantics) and is returned as-is.
func fetchAllRecent(granted []grantedSource) ([][]recentRecord, error) {
	results := make([][]recentRecord, len(granted))
	var g errgroup.Group
	for i, gs := range granted {
		i, gs := i, gs
		g.Go(func() error {
			rows, err := gs.source.fetch(gs.scope)
			if err != nil {
				return err
			}
			for j := range rows {
				rows[j].Module = gs.source.module
				rows[j].Domain = gs.source.domain
			}
			results[i] = rows
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

// RecentRecords serves the Recent records widget's data: the most
// recently-updated rows across every CRM/Sales/Purchases module the caller
// can read, merged and sorted newest-first. No dashboard_widget permission
// gate -- like Pipeline mix and KPI strip, each source enforces its own
// underlying resource's RBAC.
// GET /api/tenant/dashboard/widgets/recent-records/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) RecentRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	rawRange := r.URL.Query().Get("range")
	since, ok := parseDashboardRange(rawRange, time.Now())
	if !ok {
		fail(w, http.StatusBadRequest, "range must be one of all, 7d, 30d, quarter.")
		return
	}
	st, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	sources := buildRecentSources(ctx, st, pool, payload.ID, since, recentRecordsLimit+1)

	records, hasMore, ok, err := buildRecentRecords(
		sources,
		func(resource authz.Resource) (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, resource, authz.ActionRead)
		},
		fetchAllRecent,
		recentRecordsLimit,
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load recent records.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", "recent-records", "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read any record type.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"range":   rangeLabel,
		"records": records,
		"hasMore": hasMore,
	})
}
