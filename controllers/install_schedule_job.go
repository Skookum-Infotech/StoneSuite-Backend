package controllers

import (
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/installschedule"
)

// ListForJob GET /api/tenant/fabrication-jobs/{uuid}/install-appointments
// Requires install_appointment:read plus installation:read on the job (authFJByUUID); an own-scoped
// appointment reader only gets the visits they own or crew.
func (h *InstallScheduleOps) ListForJob(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authInstall(w, r, authz.ActionRead)
	if !ok {
		return
	}
	jobUUID := r.PathValue("uuid")
	if !validUUID(jobUUID) {
		installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallJobNotFound, nil)
		return
	}
	if _, _, _, ok := h.jobs.authFJByUUID(w, r, jobUUID, authz.ActionRead); !ok {
		return
	}
	items, err := installschedule.ListForJob(r.Context(), c.pool, jobUUID)
	if err != nil {
		writeInstallError(w, err)
		return
	}
	if c.scope != authz.ScopeAll {
		visible := c.canSee(r.Context())
		kept := make([]installschedule.Appointment, 0, len(items))
		for _, a := range items {
			if visible(a.ID) {
				kept = append(kept, a)
			}
		}
		items = kept
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "appointments": items})
}
