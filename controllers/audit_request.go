package controllers

import (
	"context"
	"net/http"

	"stonesuite-backend/workflow"
)

// sessionIDHeader carries the client session id stamped on audit rows.
const sessionIDHeader = "X-Session-Id"

// auditCtx returns the request context carrying the provenance (actor, IP,
// session, app version) a store needs to write its audit row inside its own
// transaction via workflow.LogAuditTx.
func auditCtx(r *http.Request, actorUserID string) context.Context {
	return workflow.WithAuditRequest(r.Context(), workflow.AuditRequest{
		ActorUserID: actorUserID,
		IPAddress:   clientIP(r),
		SessionID:   r.Header.Get(sessionIDHeader),
		AppVersion:  appVersion,
	})
}
