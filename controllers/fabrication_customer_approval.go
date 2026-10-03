package controllers

import (
	"encoding/json"
	"net/http"
	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// GetFabricationTemplate exposes a revision only after internal approval and customer ownership checks.
func (h *PortalDocumentOps) GetFabricationTemplate(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := portalSession(w, r)
	if !ok {
		return
	}
	identity, err := portalIdentityID(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	revision, err := fabrication.CustomerTemplate(r.Context(), pool, r.PathValue("uuid"), r.PathValue("revision"), identity)
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionRead)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success  bool                          `json:"success"`
		Template *fabrication.TemplateRevision `json:"template"`
	}{true, revision})
}

// ApproveFabricationTemplate records the authenticated customer's own decision.
func (h *PortalDocumentOps) ApproveFabricationTemplate(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := portalSession(w, r)
	if !ok {
		return
	}
	identity, err := portalIdentityID(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	var in fabrication.CommandMeta
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid approval request.")
		return
	}
	result, err := fabrication.ApproveTemplateAsCustomer(r.Context(), pool, r.PathValue("uuid"), r.PathValue("revision"), identity, in)
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionTransition)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                     `json:"success"`
		Result  fabrication.ActionResult `json:"result"`
	}{true, result})
}
