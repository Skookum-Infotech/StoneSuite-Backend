package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// DecideTemplate records an internal approval or rejection of a template revision.
func (h *FabricationOps) DecideTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var in fabrication.RevisionDecisionInput
	if !decodeFabricationInput(w, r, &in) {
		return
	}
	result, err := fabrication.DecideRevision(r.Context(), pool, id, r.PathValue("revision"), fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionTransition)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                     `json:"success"`
		Result  fabrication.ActionResult `json:"result"`
	}{true, result})
}

// RecordTemplateCustomerApproval records evidence obtained outside the application.
func (h *FabricationOps) RecordTemplateCustomerApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var in fabrication.CustomerApprovalInput
	if !decodeFabricationInput(w, r, &in) {
		return
	}
	result, err := fabrication.RecordCustomerApproval(r.Context(), pool, id, r.PathValue("revision"), fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionTransition)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                     `json:"success"`
		Result  fabrication.ActionResult `json:"result"`
	}{true, result})
}

// SetFabricationApprovalPolicy configures the sequence for new requests only.
func (h *WorkflowOps) SetFabricationApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	pool, _, identity, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	var in fabrication.ApprovalPolicy
	if !decodeFabricationInput(w, r, &in) {
		return
	}
	err := fabrication.SetApprovalPolicy(r.Context(), pool, fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	if errors.Is(err, fabrication.ErrActionPermission) {
		logSecurityEvent(r, "permission_denied", "identity", identity, "resource", string(authz.ResourceWorkflowConfig), "action", string(authz.ActionConfigure))
		fail(w, http.StatusForbidden, "You do not have permission to configure fabrication approvals.")
		return
	}
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionTransition)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool `json:"success"`
	}{true})
}

func decodeFabricationInput[T interface {
	fabrication.RevisionDecisionInput | fabrication.CustomerApprovalInput | fabrication.ApprovalPolicy
}](w http.ResponseWriter, r *http.Request, in *T) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid fabrication action input.")
		return false
	}
	return true
}
