package controllers

import (
	"net/http"

	"stonesuite-backend/authz"
)

// reportForbidden answers a report request the caller's grants do not allow:
// it records the denial as a `permission_denied` security event (the same event
// every other RBAC refusal in this package emits, so an alert on denied access
// sees report probing too) and writes the 403. Before this, the report
// handlers refused silently, which left a user working through every report
// endpoint they lacked a grant for invisible in the security log.
func reportForbidden(w http.ResponseWriter, r *http.Request, identityID string, resource authz.Resource, action authz.Action, message string) {
	logSecurityEvent(r, "permission_denied",
		"identity", identityID, "resource", string(resource), "action", string(action))
	fail(w, http.StatusForbidden, message)
}
