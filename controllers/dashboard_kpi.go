package controllers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/workflow"
)

// sparklineDays is the fixed number of trailing daily buckets shown in a
// KPI's trend line. Fixed independent of the selected dashboard range
// (unlike the delta window below) so the sparkline always reads the same
// way: "the last 7 days, day by day". It is a proxy trend -- new records
// created per day, not a reconstruction of a metric's historical value --
// since the schema carries no snapshot/history-of-aggregates table.
const sparklineDays = 7

// dashboardDeltaWindow returns the current window [curFrom, now) and the
// adjacent prior window [priorFrom, priorTo) used for the KPI strip's
// %/count deltas (and, for revenue, the windowed value itself). Both
// windows are the same width, scaled to the selected range:
// 7d/""/"all" -> 7 days each (there's no natural "prior all-time" window to
// compare "all" against, so it falls back to the same 7-day comparison as
// "7d"); 30d -> 30 days each; quarter -> 90 days each.
func dashboardDeltaWindow(raw string, now time.Time) (curFrom, priorFrom, priorTo time.Time) {
	days := 7
	if d, ok := dashboardRangeDays[raw]; ok {
		days = d
	}
	curFrom = now.AddDate(0, 0, -days)
	priorFrom = now.AddDate(0, 0, -2*days)
	priorTo = curFrom
	return curFrom, priorFrom, priorTo
}

// deltaPct returns the percent change from prior to current, rounded to the
// nearest integer, or nil when prior is zero -- there is no baseline to
// compare against (e.g. a brand-new tenant's first week), so "undefined" is
// more honest than a fake infinite/zero percentage.
func deltaPct(current, prior float64) *int {
	if prior == 0 {
		return nil
	}
	pct := int(math.Round((current - prior) / prior * 100))
	return &pct
}

// approvalModuleCounter counts one approval-chain module's pending records
// that are actually routed to the caller -- see approvalModuleCountAndOldest
// for the real implementation and dashboard_kpi_test.go for the fake used in
// tests. eligible reports whether the caller is a configured approver of
// this module at all (or a super admin), independent of whether count is
// currently 0 -- see buildNeedsApproval's doc comment for why that
// distinction matters.
type approvalModuleCounter func(key string) (count int, oldest *time.Time, eligible bool, err error)

// buildNeedsApproval sums pending-approval counts across every given
// approval-chain module key, using countAndOldest to determine each
// module's count and the caller's eligibility for it. countAndOldest is
// injected (mirrors buildPipelineMix in dashboard_pipeline.go) so this is
// testable without a real tenant pool.
//
// Authorization here is approver-table membership (mirrors
// approvalchain/engine.go's isConfiguredApprover, which is what actually
// gates the real Approve action), NOT generic RBAC resource read/scope. An
// earlier version of this metric gated on authz.Check(resource,
// ActionRead) + scope=own filtering by *ownership* -- which is wrong for an
// approval queue: a manager approving requisitions they didn't create holds
// no ownership over them, and many tenants grant "approve" without a
// separate broad "read", so that version could show zero pending items to
// a manager who genuinely had some, or (if scope=all) show every pending
// record in the tenant regardless of whether the caller was actually the
// configured approver for it. Root-caused via /debug: "manager role sees no
// Needs Approval tile at all."
//
// anyEligible is true when the caller is a configured approver (or super
// admin) for at least one of keys, even if every one of those currently has
// 0 pending -- the handler uses this to decide whether to render "0 (caught
// up)" versus omit the metric entirely for a caller it has no meaning for.
// oldest is nil when total is 0.
func buildNeedsApproval(keys []string, countAndOldest approvalModuleCounter) (total int, oldest *time.Time, anyEligible bool, err error) {
	for _, key := range keys {
		n, modOldest, eligible, err := countAndOldest(key)
		if err != nil {
			return 0, nil, false, err
		}
		if eligible {
			anyEligible = true
		}
		total += n
		if modOldest != nil && (oldest == nil || modOldest.Before(*oldest)) {
			oldest = modOldest
		}
	}
	return total, oldest, anyEligible, nil
}

// approvalModuleCountAndOldest counts key's pending-approval records that
// are actually routed to employeeID -- they're listed as an active approver
// for that record's *current status* in the module's approver table (the
// same (record_type_id, record_status_id, approver_employee_id, is_active)
// shape isConfiguredApprover checks; approvalchain/engine.go's
// isConfiguredApprover) -- or, when isSuperAdmin, every pending record
// unfiltered (mirrors engine.Approve's own isApprover||callerIsSuperAdmin
// override). Table/column names come from approvalchain's registry
// (backend-controlled constants, never client input), matching the
// string-built-but-parameterized pattern already used throughout this
// codebase's search stores.
func approvalModuleCountAndOldest(ctx context.Context, pool *pgxpool.Pool, key string, employeeID int, isSuperAdmin bool) (int, *time.Time, bool, error) {
	cfg, ok := approvalchain.ForWorkflowKey(key)
	if !ok {
		return 0, nil, false, fmt.Errorf("dashboard kpi: %q not in approvalchain registry", key)
	}
	r := cfg.Record

	if isSuperAdmin {
		q := fmt.Sprintf(`SELECT COUNT(*), MIN(%s) FROM %s WHERE %s = 'pending' AND %s IS NULL`,
			r.CreatedAtColumn, r.Table, r.ApprovalStatusColumn, r.DeletedAtColumn)
		var n int
		var oldest *time.Time
		if err := pool.QueryRow(ctx, q).Scan(&n, &oldest); err != nil {
			return 0, nil, false, fmt.Errorf("count pending %s: %w", key, err)
		}
		if n == 0 {
			oldest = nil
		}
		return n, oldest, true, nil
	}

	recordTypeID, err := approvalchain.RecordTypeIDByCode(ctx, pool, cfg.RecordTypeCode)
	if err != nil {
		return 0, nil, false, fmt.Errorf("resolve record type for %s: %w", key, err)
	}

	// eligible = is employeeID configured as an active approver for AT LEAST
	// one gate of this module, regardless of current pending count -- lets
	// the caller distinguish "0, caught up" from "not an approver here".
	var eligible bool
	if err := pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM %s WHERE record_type_id = $1 AND approver_employee_id = $2 AND is_active)`,
		cfg.ApproverTable), recordTypeID, employeeID).Scan(&eligible); err != nil {
		return 0, nil, false, fmt.Errorf("check approver eligibility for %s: %w", key, err)
	}
	if !eligible {
		return 0, nil, false, nil
	}

	q := fmt.Sprintf(`
		SELECT COUNT(*), MIN(t.%s)
		FROM %s t
		JOIN %s ap ON ap.record_type_id = $1 AND ap.record_status_id = t.%s
			AND ap.approver_employee_id = $2 AND ap.is_active
		WHERE t.%s = 'pending' AND t.%s IS NULL`,
		r.CreatedAtColumn, r.Table, cfg.ApproverTable, r.StatusColumn,
		r.ApprovalStatusColumn, r.DeletedAtColumn)
	var n int
	var oldest *time.Time
	if err := pool.QueryRow(ctx, q, recordTypeID, employeeID).Scan(&n, &oldest); err != nil {
		return 0, nil, false, fmt.Errorf("count pending %s: %w", key, err)
	}
	if n == 0 {
		oldest = nil
	}
	return n, oldest, true, nil
}

// crmPendingCountAndOldest is buildNeedsApproval's CRM (lead/prospect/
// customer) branch. CRM approval is a separate mechanism entirely from the
// approvalchain registry -- crm_workflow_approver, not <module>_approver,
// stage-scoped rather than per-status (see
// crmstore/relational_approval.go's package doc, which explicitly says CRM
// approval "is intentionally not registered" in approvalchain) -- so it was
// missed completely by a version of this metric that only iterated
// approvalchain.Keys(). Reuses crmstore.Store.PendingApprovals, the same
// method the existing GET /api/tenant/crm/{workflowKey}/approvals/pending
// endpoint already serves from -- self-contained (its own EXISTS check
// against crm_workflow_approver), so no separate authz.Check is needed
// here, matching that endpoint's own real behavior. eligible is derived
// from len(pending) > 0 rather than a dedicated "is a configured CRM
// approver" check (unlike approvalModuleCountAndOldest's registry-backed
// modules) -- a known simplification: a caller who is a CRM approver but
// currently has zero pending, and holds no eligibility on any other
// approval-chain module either, sees this metric omitted rather than
// "0, caught up". Also unlike approvalModuleCountAndOldest, there is no
// super-admin override here: PendingApprovals itself has none (a super
// admin who isn't a configured CRM approver sees no CRM records from this
// call, exactly as the existing controller endpoint above already behaves)
// and this function intentionally does not invent new super-admin
// semantics that don't exist anywhere else in the app.
func crmPendingCountAndOldest(ctx context.Context, st crmstore.Store, pool *pgxpool.Pool, identityID string) (int, *time.Time, bool, error) {
	pending, err := st.PendingApprovals(ctx, pool, identityID)
	if err != nil {
		return 0, nil, false, fmt.Errorf("list crm pending approvals: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil, false, nil
	}
	var oldest *time.Time
	for _, rec := range pending {
		if oldest == nil || rec.CreatedAt.Before(*oldest) {
			t := rec.CreatedAt
			oldest = &t
		}
	}
	return len(pending), oldest, true, nil
}

// kpiMetric is one KPI strip tile's payload. Currency/percent/arrow
// formatting and the "N this week"-style phrasing stay client-side (see
// KpiStrip.tsx) -- the backend sends raw numbers, matching Pipeline mix's
// convention. DeltaPct/DeltaCount/Sparkline are omitted (nil) for metrics
// that don't have a meaningful trend (Sales Orders, Needs Approval --
// current-state backlog counts, not additive-over-time values); SubLabel is
// omitted for metrics that don't have one (Revenue, Open Leads).
type kpiMetric struct {
	ID         string    `json:"id"`
	Value      float64   `json:"value"`
	DeltaPct   *int      `json:"deltaPct,omitempty"`
	DeltaCount *int      `json:"deltaCount,omitempty"`
	Sparkline  []float64 `json:"sparkline,omitempty"`
	SubLabel   string    `json:"subLabel,omitempty"`
	OldestDays *int      `json:"oldestDays,omitempty"`
}

// KpiStrip serves the KPI strip widget's data: Revenue, Open Leads, Sales
// Orders (in fabrication), and Needs Approval (aggregated across every
// approval-chain module the caller can see).
// GET /api/tenant/dashboard/widgets/kpi-strip/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) KpiStrip(w http.ResponseWriter, r *http.Request) {
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
	rangeSince, ok := parseDashboardRange(rawRange, time.Now())
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
	now := time.Now()
	curFrom, priorFrom, priorTo := dashboardDeltaWindow(rawRange, now)

	metrics := []kpiMetric{}

	// Revenue.
	if d, err := authz.Check(ctx, pool, payload.ID, authz.ResourceInvoice, authz.ActionRead); err == nil && d.Allowed {
		value, err := invoice.RevenueBetween(ctx, pool, string(d.Scope), payload.ID, rangeSince, time.Time{})
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load revenue.")
			return
		}
		curWindow, err := invoice.RevenueBetween(ctx, pool, string(d.Scope), payload.ID, curFrom, time.Time{})
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load revenue.")
			return
		}
		priorWindow, err := invoice.RevenueBetween(ctx, pool, string(d.Scope), payload.ID, priorFrom, priorTo)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load revenue.")
			return
		}
		sparkline, err := revenueSparkline(ctx, pool, string(d.Scope), payload.ID, now)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load revenue.")
			return
		}
		metrics = append(metrics, kpiMetric{
			ID: "revenue", Value: value, DeltaPct: deltaPct(curWindow, priorWindow), Sparkline: sparkline,
		})
	}

	// Open Leads.
	if d, err := authz.Check(ctx, pool, payload.ID, authz.ResourceLead, authz.ActionRead); err == nil && d.Allowed {
		value, err := st.CountRecordsSince(ctx, pool, "lead", string(d.Scope), payload.ID, time.Time{})
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load open leads.")
			return
		}
		curWindow, err := st.CountRecordsBetween(ctx, pool, "lead", string(d.Scope), payload.ID, curFrom, time.Time{})
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load open leads.")
			return
		}
		priorWindow, err := st.CountRecordsBetween(ctx, pool, "lead", string(d.Scope), payload.ID, priorFrom, priorTo)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load open leads.")
			return
		}
		sparkline, err := leadsSparkline(ctx, st, pool, string(d.Scope), payload.ID, now)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load open leads.")
			return
		}
		deltaCount := curWindow - priorWindow
		metrics = append(metrics, kpiMetric{
			ID: "open-leads", Value: float64(value), DeltaCount: &deltaCount, Sparkline: sparkline,
		})
	}

	// Sales Orders (in fabrication).
	if d, err := authz.Check(ctx, pool, payload.ID, authz.ResourceSalesOrder, authz.ActionRead); err == nil && d.Allowed {
		n, err := salesorder.CountInFabrication(ctx, pool, string(d.Scope), payload.ID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load sales orders.")
			return
		}
		metrics = append(metrics, kpiMetric{
			ID: "sales-orders-fabrication", Value: float64(n), SubLabel: fmt.Sprintf("%d in fabrication", n),
		})
	}

	// Needs Approval (aggregated across every approval-chain module the
	// caller is a configured approver for -- see buildNeedsApproval's doc
	// comment for why this is approver-table membership, not generic RBAC
	// read/scope).
	isSuperAdmin, err := authz.IsSuperAdmin(ctx, pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load approvals.")
		return
	}
	employeeID, foundEmployee := workflow.EmployeeIDByIdentity(ctx, pool, payload.ID)
	if foundEmployee || isSuperAdmin {
		// "crm" is a synthetic key alongside approvalchain.Keys(): CRM
		// (lead/prospect/customer) approval is a separate mechanism
		// entirely (crm_workflow_approver, not <module>_approver) that the
		// approvalchain registry explicitly excludes -- see
		// crmPendingCountAndOldest's doc comment.
		keys := append([]string{"crm"}, approvalchain.Keys()...)
		total, oldest, anyEligible, err := buildNeedsApproval(
			keys,
			func(key string) (int, *time.Time, bool, error) {
				if key == "crm" {
					return crmPendingCountAndOldest(ctx, st, pool, payload.ID)
				}
				return approvalModuleCountAndOldest(ctx, pool, key, employeeID, isSuperAdmin)
			},
		)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load approvals.")
			return
		}
		if anyEligible {
			var oldestDays *int
			subLabel := "all caught up"
			if oldest != nil {
				d := int(now.Sub(*oldest).Hours() / 24)
				oldestDays = &d
				subLabel = fmt.Sprintf("oldest %d days", d)
			}
			metrics = append(metrics, kpiMetric{
				ID: "needs-approval", Value: float64(total), SubLabel: subLabel, OldestDays: oldestDays,
			})
		}
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"range":   rangeLabel,
		"metrics": metrics,
	})
}

// revenueSparkline returns sparklineDays daily revenue sums for the trailing
// week, oldest first.
func revenueSparkline(ctx context.Context, pool *pgxpool.Pool, scope, identityID string, now time.Time) ([]float64, error) {
	out := make([]float64, sparklineDays)
	for i := 0; i < sparklineDays; i++ {
		end := now.AddDate(0, 0, -(sparklineDays - 1 - i))
		start := end.AddDate(0, 0, -1)
		v, err := invoice.RevenueBetween(ctx, pool, scope, identityID, start, end)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// leadsSparkline returns sparklineDays daily new-lead counts for the
// trailing week, oldest first.
func leadsSparkline(ctx context.Context, st crmstore.Store, pool *pgxpool.Pool, scope, identityID string, now time.Time) ([]float64, error) {
	out := make([]float64, sparklineDays)
	for i := 0; i < sparklineDays; i++ {
		end := now.AddDate(0, 0, -(sparklineDays - 1 - i))
		start := end.AddDate(0, 0, -1)
		n, err := st.CountRecordsBetween(ctx, pool, "lead", scope, identityID, start, end)
		if err != nil {
			return nil, err
		}
		out[i] = float64(n)
	}
	return out, nil
}
