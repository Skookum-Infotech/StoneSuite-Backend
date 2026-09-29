package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/crmnotify"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
)

// crmNotifyRolesRequest is the body of PUT /api/tenant/config/crm-notify-recipients:
// the role ids whose members count as the Manager and Finance recipient groups.
type crmNotifyRolesRequest struct {
	Manager []string `json:"manager"`
	Finance []string `json:"finance"`
}

// GetNotifyRecipients GET /api/tenant/config/crm-notify-recipients
// Returns the roles mapped to the Manager and Finance email groups.
func (h *CRMAdminOps) GetNotifyRecipients(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.requireConfig(w, r); !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	groups, err := crmnotify.LoadRecipientRoles(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load notification recipients.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"manager": groups[crmnotify.GroupManager],
		"finance": groups[crmnotify.GroupFinance],
	})
}

// SetNotifyRecipients PUT /api/tenant/config/crm-notify-recipients
// Replaces the Manager and Finance role mapping; an empty list clears a group,
// which then emails nobody.
func (h *CRMAdminOps) SetNotifyRecipients(w http.ResponseWriter, r *http.Request) {
	_, identityID, ok := h.requireConfig(w, r)
	if !ok {
		return
	}
	var req crmNotifyRolesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	err = crmnotify.SaveRecipientRoles(r.Context(), pool, map[crmnotify.Group][]string{
		crmnotify.GroupManager: req.Manager,
		crmnotify.GroupFinance: req.Finance,
	})
	if errors.Is(err, crmnotify.ErrInvalidRoles) {
		fail(w, http.StatusBadRequest, "One or more roles do not exist.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save notification recipients.")
		return
	}
	logSecurityEvent(r, "crm_notify_recipients_changed", "identity", identityID)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Notification recipients saved."})
}
