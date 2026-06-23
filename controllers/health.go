package controllers

import (
	"context"
	"net/http"
	"time"

	"stonesuite-backend/models"
)

// readyTimeout bounds the readiness DB probe so a slow/dead database can't hang
// the health endpoint (and therefore the orchestrator's routing decision).
const readyTimeout = 2 * time.Second

// HealthOps serves liveness and readiness probes.
//
// Liveness (/healthz) answers "is the process up?" — it never touches the
// database, so a transient DB blip doesn't cause the orchestrator to kill an
// otherwise-healthy machine. Readiness (/readyz) answers "can this instance
// serve traffic right now?" by pinging the control-plane database.
type HealthOps struct {
	// ready probes a critical dependency (the control-plane pool). nil disables
	// the dependency check (readiness then only confirms the process is up).
	ready func(ctx context.Context) error
}

// NewHealthOps builds the health handler. Pass a ping function (e.g.
// cp.Pool().Ping) to have /readyz verify database reachability; pass nil to
// skip the dependency probe.
func NewHealthOps(ready func(ctx context.Context) error) *HealthOps {
	return &HealthOps{ready: ready}
}

// Healthz is the liveness probe: 200 as long as the process can serve HTTP.
func (h *HealthOps) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "ok"})
}

// Readyz is the readiness probe: 200 when the control-plane database is
// reachable, 503 otherwise so the orchestrator stops routing to this instance.
func (h *HealthOps) Readyz(w http.ResponseWriter, r *http.Request) {
	if h.ready == nil {
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := h.ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{
			Success: false,
			Message: "Database not reachable.",
		})
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "ready"})
}
