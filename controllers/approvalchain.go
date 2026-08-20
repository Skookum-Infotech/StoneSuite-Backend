// controllers/approvalchain.go
package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// errNotApprovalChainWorkflow marks a workflow whose key has no entry in the
// approvalchain registry, so it can't have a configurable approval chain.
var errNotApprovalChainWorkflow = errors.New("this workflow does not support approval chain configuration")

// errUnknownApprovalGate marks a statusCode that isn't one of the workflow's
// configured gates.
var errUnknownApprovalGate = errors.New("statusCode is not a configured approval gate for this workflow")

// moduleConfigForWorkflow resolves a workflows.id to its approvalchain.ModuleConfig.
func moduleConfigForWorkflow(ctx context.Context, pool *pgxpool.Pool, workflowID string) (approvalchain.ModuleConfig, error) {
	wf, err := workflow.GetWorkflowByID(ctx, pool, workflowID)
	if err != nil {
		return approvalchain.ModuleConfig{}, err
	}
	cfg, ok := approvalchain.ForWorkflowKey(wf.Key)
	if !ok {
		return approvalchain.ModuleConfig{}, errNotApprovalChainWorkflow
	}
	return cfg, nil
}

// GetApprovalChain GET /api/tenant/workflows/{id}/approval-chain
func (h *WorkflowOps) GetApprovalChain(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionRead)
	if !ok {
		return
	}
	cfg, err := moduleConfigForWorkflow(r.Context(), pool, r.PathValue("id"))
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	case errors.Is(err, errNotApprovalChainWorkflow):
		fail(w, http.StatusBadRequest, errNotApprovalChainWorkflow.Error())
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	gates, err := approvalchain.GatesWithApprovers(r.Context(), pool, cfg)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load approval chain.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "gates": gates})
}

// SetApprovalChain PUT /api/tenant/workflows/{id}/approval-chain
// body {"statusCode":"PAPV","approverEmployeeIds":["12","7"]}
func (h *WorkflowOps) SetApprovalChain(w http.ResponseWriter, r *http.Request) {
	pool, _, identityID, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	cfg, err := moduleConfigForWorkflow(r.Context(), pool, r.PathValue("id"))
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	case errors.Is(err, errNotApprovalChainWorkflow):
		fail(w, http.StatusBadRequest, errNotApprovalChainWorkflow.Error())
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	var req struct {
		StatusCode          string   `json:"statusCode"`
		ApproverEmployeeIDs []string `json:"approverEmployeeIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if !cfg.HasGate(req.StatusCode) {
		fail(w, http.StatusBadRequest, errUnknownApprovalGate.Error())
		return
	}
	createdBy := resolveEmployeeID(r, identityID)
	ids, err := approvalchain.ReplaceApprovers(r.Context(), pool, cfg, req.StatusCode, req.ApproverEmployeeIDs, createdBy)
	if err != nil {
		if errors.Is(err, approvalchain.ErrUnknownApprover) {
			fail(w, http.StatusBadRequest, "One or more approverEmployeeIds do not match an active employee.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to save approval chain.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "statusCode": req.StatusCode, "approverEmployeeIds": ids})
}
