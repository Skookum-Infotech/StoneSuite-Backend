package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"stonesuite-backend/anomaly"
	"stonesuite-backend/authz"
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
type AnomalyOps struct{}

// NewAnomalyOps builds the handler set; the tenant pool comes from the request.
func NewAnomalyOps() *AnomalyOps { return &AnomalyOps{} }

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
