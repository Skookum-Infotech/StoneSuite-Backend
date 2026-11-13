package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
	"stonesuite-backend/portal"
	"stonesuite-backend/services"
)

// fabricationJobDisplayName is the customer-facing document kind for jobs.
const fabricationJobDisplayName = "Fabrication Job"

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
	pool, sess, ok := portalSession(w, r)
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
	notifyFabricationTemplateApproved(r, pool, identity, sess, r.PathValue("uuid"))
	writeJSON(w, http.StatusOK, struct {
		Success bool                     `json:"success"`
		Result  fabrication.ActionResult `json:"result"`
	}{true, result})
}

// notifyFabricationTemplateApproved best-effort-emails the approving portal user
// a confirmation of their template approval. Failures are logged only.
func notifyFabricationTemplateApproved(r *http.Request, pool *pgxpool.Pool, identityID string, sess *portal.Session, jobUUID string) {
	job, err := fabrication.Get(r.Context(), pool, jobUUID)
	if err != nil {
		slog.WarnContext(r.Context(), "portal template approval: confirmation email skipped",
			"fabricationJob", jobUUID, "error", err)
		return
	}
	ev := customerApprovedCopy().event(customerEventApproved, custResFabricationJob, fabricationJobDisplayName,
		job.Number, job.ID, 0, time.Now())
	notifyCustomerEventTo(r.Context(), identityID, sess.CustomerUUID,
		services.RecipientTarget{UserID: identityID, Email: sess.Email, Name: sess.FullName}, ev)
}

// customerApprovedCopy is the copy confirming the customer's own approval.
func customerApprovedCopy() statusCopy {
	return statusCopy{custApprovedBadge, custApprovedVerb, custApprovedSubtle,
		"Thank you for approving the template for your %s %s.", custApprovedDate, services.IconDocCheck}
}
