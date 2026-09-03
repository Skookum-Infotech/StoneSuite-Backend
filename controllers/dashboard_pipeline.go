package controllers

import (
	"math"
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
)

// dashboardRangeDays maps a dashboard widget "range" query value to how many
// days back its lower bound sits. "all" (and the empty/default value) has no
// lower bound and is handled separately in parseDashboardRange.
var dashboardRangeDays = map[string]int{
	"7d":      7,
	"30d":     30,
	"quarter": 90,
}

// pipelineStages lists the CRM stages the Pipeline mix widget counts, in
// display order, paired with the authz.Resource that gates each one --
// crmstore.CRMWorkflowKeys() gives the keys alone but not the resources, and
// this widget needs to check each stage's grant independently (see
// buildPipelineMix).
var pipelineStages = []struct {
	key      string
	resource authz.Resource
}{
	{"lead", authz.ResourceLead},
	{"prospect", authz.ResourceProspect},
	{"customer", authz.ResourceCustomer},
}

// parseDashboardRange resolves a widget "range" query value to the lower
// bound for created_at filtering. "" and "all" mean unbounded (zero
// time.Time) -- "all" is the dashboard's default range (see ConsoleHeader),
// and an omitted query param means the same thing. Any other unrecognized
// value is rejected (ok=false) rather than silently defaulting, so a typo in
// the query string surfaces as a 400 instead of quietly returning the wrong
// window.
func parseDashboardRange(raw string, now time.Time) (since time.Time, ok bool) {
	if raw == "" || raw == "all" {
		return time.Time{}, true
	}
	days, known := dashboardRangeDays[raw]
	if !known {
		return time.Time{}, false
	}
	return now.AddDate(0, 0, -days), true
}

// pipelineCloseRate is the "% closed" reading pinned at the donut's centre:
// the customer stage's share of the whole counted pipeline. Zero when the
// caller holds no grant on the customer stage (customerGranted=false) --
// there is no meaningful rate to show for a stage the caller can't see at
// all -- or when the counted pipeline is empty.
func pipelineCloseRate(counts map[string]int, customerGranted bool) int {
	if !customerGranted {
		return 0
	}
	total := counts["lead"] + counts["prospect"] + counts["customer"]
	if total == 0 {
		return 0
	}
	return int(math.Round(float64(counts["customer"]) / float64(total) * 100))
}

// pipelineSegment is one stage's live count in the Pipeline mix response.
// Presentation (label, color) is a frontend concern -- see
// src/pages/dashboard/components/PipelineDonut.tsx.
type pipelineSegment struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

// pipelineMixResult is the fully-resolved Pipeline mix widget payload.
type pipelineMixResult struct {
	Segments  []pipelineSegment
	CloseRate int
}

// buildPipelineMix resolves the Pipeline mix widget's data given two
// injectable dependencies -- check (a stage's authz decision) and count (a
// granted stage's record count) -- so the per-stage orchestration is
// testable without a real tenant pool (see dashboard_pipeline_test.go); the
// HTTP handler wires check to authz.Check and count to
// crmstore.Store.CountRecordsSince.
//
// A stage the caller holds no read grant on is omitted from Segments
// entirely rather than reported as zero, so the widget never implies "you
// have 0 customers" when it actually means "you can't see customers".
// ok=false only when none of the three stages are granted; the handler maps
// that to 403.
func buildPipelineMix(
	check func(resource authz.Resource) (authz.Decision, error),
	count func(key string, scope authz.Scope) (int, error),
) (pipelineMixResult, bool, error) {
	segments := make([]pipelineSegment, 0, len(pipelineStages))
	counts := make(map[string]int, len(pipelineStages))
	customerGranted := false

	for _, stage := range pipelineStages {
		decision, err := check(stage.resource)
		if err != nil {
			return pipelineMixResult{}, false, err
		}
		if !decision.Allowed {
			continue
		}
		n, err := count(stage.key, decision.Scope)
		if err != nil {
			return pipelineMixResult{}, false, err
		}
		counts[stage.key] = n
		segments = append(segments, pipelineSegment{ID: stage.key, Count: n})
		if stage.key == "customer" {
			customerGranted = true
		}
	}

	if len(segments) == 0 {
		return pipelineMixResult{}, false, nil
	}
	return pipelineMixResult{
		Segments:  segments,
		CloseRate: pipelineCloseRate(counts, customerGranted),
	}, true, nil
}

// PipelineMix serves the Pipeline mix widget's data: how many live CRM
// records sit in each stage (lead/prospect/customer), optionally narrowed to
// those created within the requested range, each stage independently scoped
// to the caller's RBAC grant on that stage's resource.
// GET /api/tenant/dashboard/widgets/pipeline-donut/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) PipelineMix(w http.ResponseWriter, r *http.Request) {
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

	result, ok, err := buildPipelineMix(
		func(resource authz.Resource) (authz.Decision, error) {
			return authz.Check(r.Context(), pool, payload.ID, resource, authz.ActionRead)
		},
		func(key string, scope authz.Scope) (int, error) {
			return st.CountRecordsSince(r.Context(), pool, key, string(scope), payload.ID, since)
		},
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load pipeline counts.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", "lead,prospect,customer", "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read any CRM stage.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"range":     rangeLabel,
		"segments":  result.Segments,
		"closeRate": result.CloseRate,
	})
}
