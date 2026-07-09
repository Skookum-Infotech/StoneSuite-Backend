package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// CRMAdminOps handles workspace-admin CRM configuration: switching the tenant's
// database design (design_version) and managing the configurable approvers used
// by the customer Closed-Won approval flow. All endpoints require the
// workflow_config:configure permission (super admin by default).
type CRMAdminOps struct {
	cp *tenancy.ControlPlane
}

// NewCRMAdminOps constructs the handler group.
func NewCRMAdminOps(cp *tenancy.ControlPlane) *CRMAdminOps { return &CRMAdminOps{cp: cp} }

// requireConfig checks the caller may configure workspace settings. Returns the
// resolved tenant + identity on success.
func (h *CRMAdminOps) requireConfig(w http.ResponseWriter, r *http.Request) (*tenancy.Tenant, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to configure this workspace.")
		return nil, "", false
	}
	return tenant, payload.ID, true
}

// ---- design version ---------------------------------------------------------

// GetDesignVersion GET /api/tenant/admin/design-version
func (h *CRMAdminOps) GetDesignVersion(w http.ResponseWriter, r *http.Request) {
	tenant, _, ok := h.requireConfig(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"designVersion": tenant.DesignVersion,
		"available":     []string{tenancy.DesignV1, tenancy.DesignV2},
	})
}

// SetDesignVersion POST /api/tenant/admin/design-version  body {"designVersion":"v2"}
// Both schemas coexist in the tenant database, so this is a flag flip with no
// data migration; CRM requests immediately use the selected design.
func (h *CRMAdminOps) SetDesignVersion(w http.ResponseWriter, r *http.Request) {
	tenant, _, ok := h.requireConfig(w, r)
	if !ok {
		return
	}
	var req struct {
		DesignVersion string `json:"designVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.DesignVersion != tenancy.DesignV1 && req.DesignVersion != tenancy.DesignV2 {
		fail(w, http.StatusBadRequest, "designVersion must be 'v1' or 'v2'.")
		return
	}
	if err := h.cp.SetTenantDesignVersion(r.Context(), tenant.ID, req.DesignVersion); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update design version.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       "Design version updated.",
		"designVersion": req.DesignVersion,
	})
}

// ---- approver configuration (v2 crm_workflow_approver) ----------------------

type approverRow struct {
	ID                 int    `json:"id"`
	RecordTypeCode     string `json:"recordTypeCode"`
	CrmStatusCode      string `json:"crmStatusCode"`
	ApproverEmployeeID int    `json:"approverEmployeeId"`
	ApproverName       string `json:"approverName"`
	IsActive           bool   `json:"isActive"`
}

// ListApprovers GET /api/tenant/config/approvers
func (h *CRMAdminOps) ListApprovers(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.requireConfig(w, r); !ok {
		return
	}
	pool, _ := tenancy.PoolFromContext(r.Context())
	rows, err := pool.Query(r.Context(), `
		SELECT a.crm_workflow_approver_id, rt.record_type_code, COALESCE(cs.crm_status_code,''),
		       a.approver_employee_id,
		       TRIM(COALESCE(e.employee_first_name,'') || ' ' || COALESCE(e.employee_last_name,'')),
		       a.is_active
		FROM crm_workflow_approver a
		JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
		LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = a.crm_status_id
		LEFT JOIN employee e ON e.employee_id = a.approver_employee_id
		ORDER BY a.crm_workflow_approver_id`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list approvers.")
		return
	}
	defer rows.Close()
	out := []approverRow{}
	for rows.Next() {
		var a approverRow
		if err := rows.Scan(&a.ID, &a.RecordTypeCode, &a.CrmStatusCode, &a.ApproverEmployeeID, &a.ApproverName, &a.IsActive); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read approvers.")
			return
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "approvers": out})
}

// CreateApprover POST /api/tenant/config/approvers
// body {"recordTypeCode":"CUST","crmStatusCode":"CCLW","approverEmployeeId":2}
func (h *CRMAdminOps) CreateApprover(w http.ResponseWriter, r *http.Request) {
	_, identityID, ok := h.requireConfig(w, r)
	if !ok {
		return
	}
	var req struct {
		RecordTypeCode     string `json:"recordTypeCode"`
		CrmStatusCode      string `json:"crmStatusCode"`
		ApproverEmployeeID int    `json:"approverEmployeeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApproverEmployeeID <= 0 {
		fail(w, http.StatusBadRequest, "approverEmployeeId is required.")
		return
	}
	if req.RecordTypeCode == "" {
		req.RecordTypeCode = "CUST"
	}
	pool, _ := tenancy.PoolFromContext(r.Context())

	// Resolve the creating employee (best-effort; may be NULL).
	var createdBy *int
	if id := resolveEmployeeID(r, identityID); id > 0 {
		createdBy = &id
	}
	_, err := pool.Exec(r.Context(), `
		INSERT INTO crm_workflow_approver (record_type_id, crm_status_id, approver_employee_id, created_by)
		VALUES (
			(SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1),
			(SELECT crm_status_id FROM lkp_crm_status cs
			   JOIN lkp_record_type rt ON rt.record_type_id = cs.crm_status_record_type
			   WHERE cs.crm_status_code = NULLIF($2,'') AND rt.record_type_code = $1),
			$3, $4)
		ON CONFLICT (record_type_id, crm_status_id, approver_employee_id) DO NOTHING`,
		req.RecordTypeCode, req.CrmStatusCode, req.ApproverEmployeeID, createdBy)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to add approver.")
		return
	}
	writeJSON(w, http.StatusCreated, models.APIResponse{Success: true, Message: "Approver added."})
}

// DeleteApprover DELETE /api/tenant/config/approvers/{id}
func (h *CRMAdminOps) DeleteApprover(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.requireConfig(w, r); !ok {
		return
	}
	pool, _ := tenancy.PoolFromContext(r.Context())
	tag, err := pool.Exec(r.Context(),
		`DELETE FROM crm_workflow_approver WHERE crm_workflow_approver_id = $1`, r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to remove approver.")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "Approver not found.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Approver removed."})
}

// resolveEmployeeID best-effort maps the caller's identity to an employee_id.
func resolveEmployeeID(r *http.Request, identityID string) int {
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		return 0
	}
	var id int
	if err := pool.QueryRow(r.Context(), `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id); err != nil {
		return 0
	}
	return id
}

// ---- workflow-scoped approver config (GET/PATCH /api/tenant/workflows/{id}/approvers) --

// errNotCRMWorkflow marks a workflow with no CRM record-type mapping (only
// lead/prospect/customer do), so it can't have configured approvers.
var errNotCRMWorkflow = errors.New("this workflow does not support approver configuration")

// errUnknownApproverUser marks an approverUserId that doesn't resolve to an
// active employee in this tenant.
var errUnknownApproverUser = errors.New("one or more approverUserIds do not match an active employee")

// recordTypeCodeForWorkflow resolves a workflow id to its v2 record_type_code
// (e.g. "CUST" for the customer workflow) for the approver-config endpoints,
// which only have a workflow id, not a CRM record, to resolve from.
func recordTypeCodeForWorkflow(ctx context.Context, pool *pgxpool.Pool, workflowID string) (string, error) {
	wf, err := workflow.GetWorkflowByID(ctx, pool, workflowID)
	if err != nil {
		return "", err
	}
	code, ok := crmstore.RecordTypeCodeForKey(wf.Key)
	if !ok {
		return "", errNotCRMWorkflow
	}
	return code, nil
}

// activeApproverUserIDs returns the tenant user ids of every active,
// any-status ("wildcard") approver currently configured for recordTypeCode.
func activeApproverUserIDs(ctx context.Context, pool *pgxpool.Pool, recordTypeCode string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id::text FROM crm_workflow_approver a
		JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
		JOIN employee e ON e.employee_id = a.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE rt.record_type_code = $1 AND a.crm_status_id IS NULL AND a.is_active
		ORDER BY a.crm_workflow_approver_id`, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("list approver user ids: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// replaceActiveApprovers sets the full any-status ("wildcard") active
// approver set for recordTypeCode to exactly userIDs, replacing whatever was
// configured before. Runs in one transaction so the swap is atomic. No count
// cap is enforced — the 2-approver limit is a UI concern.
func replaceActiveApprovers(ctx context.Context, pool *pgxpool.Pool, recordTypeCode string, userIDs []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	empIDs := make([]int, 0, len(userIDs))
	for _, uid := range userIDs {
		var empID int
		if err := tx.QueryRow(ctx, `
			SELECT e.employee_id FROM employee e
			JOIN users u ON u.id = e.employee_user_id
			WHERE u.id = $1 AND e.employee_deleted_at IS NULL`, uid).Scan(&empID); err != nil {
			return errUnknownApproverUser
		}
		empIDs = append(empIDs, empID)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM crm_workflow_approver a
		USING lkp_record_type rt
		WHERE a.record_type_id = rt.record_type_id
		  AND rt.record_type_code = $1 AND a.crm_status_id IS NULL`, recordTypeCode); err != nil {
		return fmt.Errorf("clear existing approvers: %w", err)
	}
	for _, empID := range empIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO crm_workflow_approver (record_type_id, crm_status_id, approver_employee_id)
			VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1), NULL, $2)`,
			recordTypeCode, empID); err != nil {
			return fmt.Errorf("insert approver: %w", err)
		}
	}
	return tx.Commit(ctx)
}
