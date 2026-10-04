package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// Templates lists revisions after installation read and row-scope checks.
func (h *FabricationOps) Templates(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	revisions, err := fabrication.Templates(r.Context(), pool, id)
	if err != nil {
		fjFail(w, err, "Failed to load template revisions.")
		return
	}
	for i := range revisions {
		revisions[i].AvailableActions, err = fabrication.TemplateActions(r.Context(), pool, revisions[i], identity, resolveEmployeeID(r, identity))
		if err != nil {
			fjFail(w, err, "Failed to load template actions.")
			return
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Success   bool                           `json:"success"`
		Templates []fabrication.TemplateRevision `json:"templates"`
	}{true, revisions})
}

// SubmitTemplate submits a measured revision, with a fresh authorization check in its transaction.
func (h *FabricationOps) SubmitTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.SubmitTemplateInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid template submission.")
		return
	}
	result, err := fabrication.SubmitTemplate(r.Context(), pool, id, fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionUpdate)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                     `json:"success"`
		Result  fabrication.ActionResult `json:"result"`
	}{true, result})
}

const templateBodyLimit = 1 << 20

func fabricationActionFail(w http.ResponseWriter, r *http.Request, err error, identity string, action authz.Action) {
	switch {
	case errors.Is(err, fabrication.ErrNotApprover):
		logSecurityEvent(r, "approval_denied", "identity", identity, "action", string(action), "resource", "installation", "record", r.PathValue("uuid"))
		fail(w, http.StatusForbidden, "Only the next configured approver can decide this request.")
	case errors.Is(err, fabrication.ErrActionPermission):
		logSecurityEvent(r, "permission_denied", "identity", identity, "action", string(action), "resource", "installation", "record", r.PathValue("uuid"))
		fail(w, http.StatusForbidden, "You do not have permission to perform this action.")
	case errors.Is(err, fabrication.ErrNotFound):
		logSecurityEvent(r, "idor_denied", "identity", identity, "action", string(action), "resource", "installation", "record", r.PathValue("uuid"))
		fail(w, http.StatusNotFound, "Fabrication job not found.")
	case errors.Is(err, fabrication.ErrActionConflict):
		fail(w, http.StatusConflict, err.Error())
	default:
		fjFail(w, err, "Failed to complete fabrication action.")
	}
}
