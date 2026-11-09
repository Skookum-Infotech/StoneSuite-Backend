package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// maxCustomBodyBytes bounds a custom-report request: a definition is a few KB
// at its largest (20 columns, 10 filters, 5 totals).
const maxCustomBodyBytes = 64 << 10

// customCaller is a signed-in caller who holds the custom_report grant the
// endpoint needs.
type customCaller struct {
	pool       *pgxpool.Pool
	tenantID   string
	employeeID int // 0 when the identity has no employee row
	access     customAccess
}

// authorizeCustom runs the checks every Custom Reports endpoint shares:
// signed in, tenant resolved, and custom_report:<action>. On failure it has
// already written the response and ok is false.
func (h *ReportsOps) authorizeCustom(w http.ResponseWriter, r *http.Request, action authz.Action) (customCaller, bool) {
	return authorizeCustomCaller(w, r, action)
}

// authorizeCustomCaller is authorizeCustom without a receiver, so the AI
// handler (which lives on AIOps) applies the identical checks.
func authorizeCustomCaller(w http.ResponseWriter, r *http.Request, action authz.Action) (customCaller, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return customCaller{}, false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return customCaller{}, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return customCaller{}, false
	}
	grants, err := authz.EffectiveGrants(r.Context(), pool, payload.ID)
	if err != nil {
		slog.Error("custom report grant lookup failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return customCaller{}, false
	}
	if !authz.DecideAny(grants, []authz.Resource{authz.ResourceCustomReport}, action).Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to use custom reports.")
		return customCaller{}, false
	}
	employeeID, _ := workflow.EmployeeIDByIdentity(r.Context(), pool, payload.ID)
	return customCaller{
		pool:       pool,
		tenantID:   tenant.ID,
		employeeID: employeeID,
		access:     buildCustomAccess(grants, tenant.DesignVersion == tenancy.DesignV2),
	}, true
}

// requireEmployee refuses writes from an identity that cannot own a report.
func (c customCaller) requireEmployee(w http.ResponseWriter) bool {
	if c.employeeID == 0 {
		fail(w, http.StatusForbidden, "Your account has no employee profile, so it cannot own reports.")
		return false
	}
	return true
}

// source resolves a definition's source for this caller, answering 400 for an
// unknown key and 403 when the caller no longer has that module's report grant.
func (c customCaller) source(w http.ResponseWriter, key string) (reports.Source, bool) {
	src, ok := reports.SourceByKey(key)
	if !ok {
		fail(w, http.StatusBadRequest, "Unknown source.")
		return reports.Source{}, false
	}
	if !c.access.canUse(src) {
		fail(w, http.StatusForbidden, fmt.Sprintf("You no longer have access to %s.", src.Label))
		return reports.Source{}, false
	}
	return src, true
}

// writeCustomError answers with the status customReportErrorStatus picks and
// logs only the unexpected ones.
func writeCustomError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := customReportErrorStatus(err)
	if status == http.StatusInternalServerError {
		slog.Error("custom report request failed", "request_id", middleware.RequestIDFromContext(r.Context()), "path", r.URL.Path, "err", err)
	}
	fail(w, status, msg)
}

// decodeCustomBody reads a bounded JSON body, answering 413/400 on failure.
func decodeCustomBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCustomBodyBytes)).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "Request is too large.")
			return false
		}
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return false
	}
	return true
}

type catalogFieldDTO struct {
	Key     string            `json:"key"`
	Label   string            `json:"label"`
	Group   string            `json:"group"`
	Type    reports.FieldType `json:"type"`
	Options []string          `json:"options,omitempty"`
}

type catalogSourceDTO struct {
	Key    string            `json:"key"`
	Label  string            `json:"label"`
	Module string            `json:"module"`
	Fields []catalogFieldDTO `json:"fields"`
}

// statusOptionsCache loads each status list once per request.
type statusOptionsCache struct {
	ctx  context.Context
	pool *pgxpool.Pool
	seen map[string][]string
}

func (s *statusOptionsCache) get(code string) ([]string, error) {
	if opts, ok := s.seen[code]; ok {
		return opts, nil
	}
	opts, err := reports.StatusOptions(s.ctx, s.pool, code)
	if err != nil {
		return nil, err
	}
	s.seen[code] = opts
	return opts, nil
}

func buildCatalogDTO(sources []reports.Source, options *statusOptionsCache) ([]catalogSourceDTO, error) {
	out := make([]catalogSourceDTO, 0, len(sources))
	for _, s := range sources {
		dto := catalogSourceDTO{Key: s.Key, Label: s.Label, Module: s.Module, Fields: make([]catalogFieldDTO, 0, len(s.Fields))}
		for _, f := range s.Fields {
			field := catalogFieldDTO{Key: f.Key, Label: f.Label, Group: f.Group, Type: f.Type}
			if f.Type == reports.FieldStatus {
				field.Options = f.Options
				if len(field.Options) == 0 {
					opts, err := options.get(f.StatusRecordType)
					if err != nil {
						return nil, err
					}
					field.Options = opts
				}
			}
			dto.Fields = append(dto.Fields, field)
		}
		out = append(out, dto)
	}
	return out, nil
}

// CustomCatalog lists the sources and fields the caller can build reports on.
// GET /api/tenant/reports/custom/catalog
func (h *ReportsOps) CustomCatalog(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionRead)
	if !ok {
		return
	}
	cache := &statusOptionsCache{ctx: r.Context(), pool: caller.pool, seen: map[string][]string{}}
	sources, err := buildCatalogDTO(caller.access.sources(), cache)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sources": sources})
}

type customRunBody struct {
	Definition reports.Definition `json:"definition"`
	Limit      *int               `json:"limit"`
}

// CustomRun runs a definition for the caller's own scope and returns its rows.
// POST /api/tenant/reports/custom/run  body {definition, limit?}
func (h *ReportsOps) CustomRun(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var body customRunBody
	if !decodeCustomBody(w, r, &body) {
		return
	}
	limit, err := parseRunLimit(body.Limit)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	src, ok := caller.source(w, body.Definition.Source)
	if !ok {
		return
	}
	scope := scopeArgsFor(caller.access.scope(src.Resource), caller.employeeID)
	result, err := reports.Run(r.Context(), caller.pool, src, body.Definition, scope, limit)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

// CustomList returns the caller's reports, then everyone's shared ones.
// GET /api/tenant/reports/custom
func (h *ReportsOps) CustomList(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionRead)
	if !ok {
		return
	}
	list, err := reports.ListReports(r.Context(), caller.pool, caller.employeeID)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "reports": list})
}

// CustomGet returns one report the caller may see.
// GET /api/tenant/reports/custom/{uuid}
func (h *ReportsOps) CustomGet(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionRead)
	if !ok {
		return
	}
	report, err := reports.GetReport(r.Context(), caller.pool, caller.employeeID, r.PathValue("uuid"))
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "report": report})
}

type customReportBody struct {
	Name       string             `json:"name"`
	Definition reports.Definition `json:"definition"`
	Shared     bool               `json:"shared"`
}

// readReportBody decodes and checks a create/update body: name, source access
// and the definition itself.
func (c customCaller) readReportBody(w http.ResponseWriter, r *http.Request) (customReportBody, bool) {
	var body customReportBody
	if !decodeCustomBody(w, r, &body) {
		return body, false
	}
	name, err := validateReportName(body.Name)
	if err != nil {
		writeCustomError(w, r, err)
		return body, false
	}
	body.Name = name
	src, ok := c.source(w, body.Definition.Source)
	if !ok {
		return body, false
	}
	if err := reports.Validate(body.Definition, src); err != nil {
		writeCustomError(w, r, err)
		return body, false
	}
	return body, true
}

// CustomCreate saves a new private (or shared) report.
// POST /api/tenant/reports/custom  body {name, definition, shared}
func (h *ReportsOps) CustomCreate(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionCreate)
	if !ok || !caller.requireEmployee(w) {
		return
	}
	body, ok := caller.readReportBody(w, r)
	if !ok {
		return
	}
	report, err := reports.CreateReport(r.Context(), caller.pool, caller.employeeID, body.Name, body.Definition, body.Shared)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "report": report})
}

// CustomUpdate rewrites a report its owner saved.
// PUT /api/tenant/reports/custom/{uuid}  body {name, definition, shared}
func (h *ReportsOps) CustomUpdate(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionCreate)
	if !ok || !caller.requireEmployee(w) {
		return
	}
	body, ok := caller.readReportBody(w, r)
	if !ok {
		return
	}
	report, err := reports.UpdateReport(r.Context(), caller.pool, caller.employeeID, r.PathValue("uuid"), body.Name, body.Definition, body.Shared)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "report": report})
}

// CustomDelete removes a report its owner saved.
// DELETE /api/tenant/reports/custom/{uuid}
func (h *ReportsOps) CustomDelete(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionCreate)
	if !ok || !caller.requireEmployee(w) {
		return
	}
	if err := reports.DeleteReport(r.Context(), caller.pool, caller.employeeID, r.PathValue("uuid")); err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// CustomCopy saves a private copy of a report the caller can see (their own or
// shared), so a shared report can be adapted without touching the original.
// POST /api/tenant/reports/custom/{uuid}/copy
func (h *ReportsOps) CustomCopy(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorizeCustom(w, r, authz.ActionCreate)
	if !ok || !caller.requireEmployee(w) {
		return
	}
	original, err := reports.GetReport(r.Context(), caller.pool, caller.employeeID, r.PathValue("uuid"))
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	src, ok := caller.source(w, original.Definition.Source)
	if !ok {
		return
	}
	// A stored definition can name a field the catalog has since lost; check it
	// before it is copied, like every other path that stores one.
	if err := reports.Validate(original.Definition, src); err != nil {
		writeCustomError(w, r, err)
		return
	}
	report, err := reports.CreateReport(r.Context(), caller.pool, caller.employeeID, copyName(original.Name), original.Definition, false)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "report": report})
}
