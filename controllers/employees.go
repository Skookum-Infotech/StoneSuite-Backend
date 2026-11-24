// controllers/employees.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/employee"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// EmployeeOps serves Configuration > Employee: the tenant's staff directory.
// A new employee waits for the approvers configured under Configure > Workflows
// > Employee (none configured = Active at once), and an Active employee has a
// Vendor record of type "Employee". Creating an employee never creates a login.
//
// Employees are tenant-wide configuration data with no owner, so the grant
// alone decides access: there is no per-record scope or IDOR check, the same as
// the users and roles admin endpoints.
//
// Routes:
//
//	GET    /api/tenant/employees                    — list
//	POST   /api/tenant/employees/search             — filter + sort + search
//	POST   /api/tenant/employees                    — create
//	GET    /api/tenant/employees/{uuid}             — get + approval info
//	PATCH  /api/tenant/employees/{uuid}             — edit
//	POST   /api/tenant/employees/{uuid}/transition  — activate / deactivate
//	POST   /api/tenant/employees/{uuid}/approve     — approver sign-off
//	POST   /api/tenant/employees/{uuid}/reject      — approver veto
//	GET    /api/tenant/employees/{uuid}/history     — status trail
type EmployeeOps struct{}

// NewEmployeeOps constructs the handler group.
func NewEmployeeOps() *EmployeeOps { return &EmployeeOps{} }

// authEmployee resolves JWT + tenant pool + the employee:<action> grant, and
// refuses a mutation from a disabled account (authz.Check does not filter on
// user status).
func (h *EmployeeOps) authEmployee(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceEmployee, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceEmployee), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" employees.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// List GET /api/tenant/employees
func (h *EmployeeOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authEmployee(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.writePage(w, r, pool, req, "Failed to list employees.")
}

// Search POST /api/tenant/employees/search
func (h *EmployeeOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authEmployee(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	h.writePage(w, r, pool, req, "Failed to search employees.")
}

func (h *EmployeeOps) writePage(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request, failMsg string) {
	page, err := employee.Search(r.Context(), pool, req)
	if err != nil {
		employeeFail(w, err, failMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Create POST /api/tenant/employees
func (h *EmployeeOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authEmployee(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in employee.CreateEmployeeInput
	raw, ok := readJSONBody(w, r, &in)
	if !ok {
		return
	}
	key, ok := idempotencyKeyFromRequest(w, r)
	if !ok {
		return
	}
	if key != "" {
		in.IdempotencyKey = callerIdempotencyKey(identityID, key)
		in.RequestHash = idempotency.HashRequest(raw)
	}
	emp, err := employee.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		employeeFail(w, err, "Failed to create employee.")
		return
	}
	auditEmployee(r, pool, identityID, "create", emp.ID, nil, emp)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "employee": emp})
}

// Get GET /api/tenant/employees/{uuid}
func (h *EmployeeOps) Get(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, ok := h.authEmployee(w, r, authz.ActionRead)
	if !ok {
		return
	}
	emp, err := employee.Get(r.Context(), pool, uuid)
	if err != nil {
		employeeFail(w, err, "Failed to load employee.")
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		employeeFail(w, err, "Failed to load employee.")
		return
	}
	info, err := employee.GetApprovalInfo(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		employeeFail(w, err, "Failed to load employee.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "employee": emp, "approval": info})
}

// Update PATCH /api/tenant/employees/{uuid}
func (h *EmployeeOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, ok := h.authEmployee(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in employee.UpdateEmployeeInput
	if _, ok := readJSONBody(w, r, &in); !ok {
		return
	}
	before, _ := employee.Get(r.Context(), pool, uuid)
	after, err := employee.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		employeeFail(w, err, "Failed to update employee.")
		return
	}
	auditEmployee(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "employee": after})
}

// Transition POST /api/tenant/employees/{uuid}/transition  body {"toStatusCode":"ACTV"|"INAC"}
func (h *EmployeeOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	var req struct {
		ToStatusCode string `json:"toStatusCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	pool, identityID, ok := h.authEmployee(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	before, _ := employee.Get(r.Context(), pool, uuid)
	emp, err := employee.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		employeeFail(w, err, "Failed to change the employee's status.")
		return
	}
	auditEmployee(r, pool, identityID, "transition", uuid, before, emp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "employee": emp})
}

// Approve POST /api/tenant/employees/{uuid}/approve
func (h *EmployeeOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, ok := h.authEmployee(w, r, authz.ActionApprove)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		employeeFail(w, err, "Failed to approve employee.")
		return
	}
	emp, err := employee.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		if errors.Is(err, employee.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		employeeFail(w, err, "Failed to approve employee.")
		return
	}
	auditEmployee(r, pool, identityID, "approve", uuid, nil, emp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "employee": emp})
}

// Reject POST /api/tenant/employees/{uuid}/reject  body {"reason":"..."}
func (h *EmployeeOps) Reject(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, ok := h.authEmployee(w, r, authz.ActionApprove)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		employeeFail(w, err, "Failed to reject employee.")
		return
	}
	emp, err := employee.Reject(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin, req.Reason)
	if err != nil {
		if errors.Is(err, employee.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		employeeFail(w, err, "Failed to reject employee.")
		return
	}
	auditEmployee(r, pool, identityID, "reject", uuid, nil, emp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "employee": emp})
}

// History GET /api/tenant/employees/{uuid}/history
func (h *EmployeeOps) History(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, ok := h.authEmployee(w, r, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := employee.History(r.Context(), pool, uuid)
	if err != nil {
		employeeFail(w, err, "Failed to load the employee's history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": entries})
}
