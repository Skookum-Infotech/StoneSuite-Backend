package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/anomaly"
	"stonesuite-backend/authz"
	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// maxAnomalyBodyBytes caps the review request body.
const maxAnomalyBodyBytes = 8 << 10

var anomalyUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AnomalyOps serves the Anomalies page: GET /api/tenant/anomalies and
// PATCH /api/tenant/anomalies/{uuid}.
//
// There is deliberately no RBAC resource here. A finding is addressed to
// specific people (the record owner, the module's approvers, super admins), so
// visibility is the recipient list itself: a super admin sees every finding,
// anyone else only those sent to them, and a finding that is not theirs is a
// 404 so its existence is not revealed.
type AnomalyOps struct {
	scanner  AnomalyScanner
	fallback *time.Location
	now      func() time.Time
}

// NewAnomalyOps builds the handler set; the tenant pool comes from the request.
// scanner runs the on-demand scan; fallback is the timezone used for a tenant
// whose company profile has none.
func NewAnomalyOps(scanner AnomalyScanner, fallback *time.Location) *AnomalyOps {
	return &AnomalyOps{scanner: scanner, fallback: fallback, now: time.Now}
}

// viewer resolves who is asking, answering the request itself on failure.
func (h *AnomalyOps) viewer(w http.ResponseWriter, r *http.Request) (anomaly.Viewer, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return anomaly.Viewer{}, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return anomaly.Viewer{}, false
	}
	seesAll, err := authz.IsSuperAdmin(r.Context(), pool, payload.ID)
	if err != nil {
		slog.Error("anomaly viewer role check", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return anomaly.Viewer{}, false
	}
	return anomaly.Viewer{IdentityID: payload.ID, SeesAll: seesAll}, true
}

// parseAnomalyFilter validates the query string; the message is safe to return.
func parseAnomalyFilter(r *http.Request) (anomaly.ListFilter, string) {
	q := r.URL.Query()
	f := anomaly.ListFilter{Limit: anomaly.DefaultPageSize}
	if s := q.Get("status"); s != "" {
		status := anomaly.Status(s)
		if !status.Valid() {
			return f, "Invalid status."
		}
		f.Status = status
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			return f, "limit must be a positive integer."
		}
		f.Limit = min(n, anomaly.MaxPageSize, query.MaxLimit)
	}
	if c := q.Get("cursor"); c != "" {
		cur, err := anomaly.DecodeCursor(c)
		if err != nil {
			return f, "Invalid cursor."
		}
		f.Cursor = cur
	}
	return f, ""
}

// List handles GET /api/tenant/anomalies.
func (h *AnomalyOps) List(w http.ResponseWriter, r *http.Request) {
	v, ok := h.viewer(w, r)
	if !ok {
		return
	}
	filter, msg := parseAnomalyFilter(r)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	page, err := anomaly.List(r.Context(), pool, v, filter)
	if err != nil {
		slog.Error("list anomalies", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load anomalies.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "items": page.Items, "nextCursor": page.NextCursor})
}

// UpdateStatus handles PATCH /api/tenant/anomalies/{uuid}.
func (h *AnomalyOps) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	v, ok := h.viewer(w, r)
	if !ok {
		return
	}
	id := r.PathValue("uuid")
	if !anomalyUUIDRE.MatchString(id) {
		fail(w, http.StatusNotFound, "Anomaly not found.")
		return
	}
	var body struct {
		Status anomaly.Status `json:"status"`
		Note   string         `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAnomalyBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || !body.Status.Valid() {
		fail(w, http.StatusBadRequest, "Body must be {\"status\": \"open|reviewed|dismissed|confirmed\", \"note\": \"...\"}.")
		return
	}
	note := strings.TrimSpace(body.Note)
	if len([]rune(note)) > anomaly.MaxNoteLen {
		fail(w, http.StatusBadRequest, "Note is too long.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	item, err := anomaly.UpdateStatus(r.Context(), pool, v, id, body.Status, note)
	if errors.Is(err, anomaly.ErrNotFound) {
		fail(w, http.StatusNotFound, "Anomaly not found.")
		return
	}
	if err != nil {
		slog.Error("update anomaly", "anomaly_id", id, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to update anomaly.")
		return
	}
	tenantID := ""
	if t, terr := tenancy.TenantFromContext(r.Context()); terr == nil {
		tenantID = t.ID
	}
	slog.Info("anomaly_status_changed",
		"tenant_id", tenantID, "anomaly_id", id, "identity", v.IdentityID, "status", string(body.Status))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "item": item})
}

// AnomalyScanner runs one anomaly scan and alert pass for a tenant on demand
// (automation.AnomalyScan).
type AnomalyScanner interface {
	ScanNow(ctx context.Context, t *tenancy.Tenant, pool *pgxpool.Pool, now time.Time, fallback *time.Location) (automation.Result, error)
}

// anomalyScanTimeout bounds one on-demand scan, including its alert sends.
const anomalyScanTimeout = 90 * time.Second

// scanNowResponse is the body of a successful scan: counts only, never findings
// (those are read through the normal, recipient-scoped list).
type scanNowResponse struct {
	Success    bool `json:"success"`
	Raised     int  `json:"raised"`
	AlreadyHad int  `json:"alreadyKnown"`
	AlertsSent int  `json:"alertsSent"`
	AlertsFail int  `json:"alertsFailed"`
}

// scanNowOutcome maps a scan result to the HTTP answer. The messages are safe to
// show to the person who asked.
func scanNowOutcome(res automation.Result, err error) (int, any) {
	switch {
	case errors.Is(err, automation.ErrAnomalyScanOff):
		return http.StatusConflict, map[string]any{"success": false, "message": "Anomaly scanning is switched off for this workspace. Turn it on first."}
	case errors.Is(err, automation.ErrAnomalyScanBusy):
		return http.StatusConflict, map[string]any{"success": false, "message": "A scan is already running. Try again in a moment."}
	case err != nil:
		return http.StatusInternalServerError, map[string]any{"success": false, "message": "The scan did not finish. Try again."}
	}
	return http.StatusOK, scanNowResponse{
		Success: true, Raised: res.Transitioned, AlreadyHad: res.Skipped, AlertsSent: res.Sent, AlertsFail: res.Failed,
	}
}

// ScanNow handles POST /api/tenant/anomalies/scan: run the scan and alert pass
// for this workspace now instead of waiting for the next 15-minute tick. It needs
// automation:configure (the same permission as the on/off switch).
func (h *AnomalyOps) ScanNow(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceAutomation, authz.ActionConfigure)
	if err != nil {
		slog.Error("anomaly scan permission check", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceAutomation), "action", string(authz.ActionConfigure))
		fail(w, http.StatusForbidden, "You do not have permission to run an anomaly scan.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), anomalyScanTimeout)
	defer cancel()
	res, err := h.scanner.ScanNow(ctx, tenant, pool, h.now(), h.fallback)
	status, body := scanNowOutcome(res, err)
	if status == http.StatusInternalServerError {
		slog.Error("anomaly scan now", "tenant_id", tenant.ID, "error", err)
	} else {
		slog.Info("anomaly_scan_now", "tenant_id", tenant.ID, "identity", payload.ID, "status", status,
			"raised", res.Transitioned, "already_known", res.Skipped, "alerts_sent", res.Sent)
	}
	writeJSON(w, status, body)
}
