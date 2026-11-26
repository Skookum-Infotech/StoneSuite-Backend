package controllers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
)

const (
	msgSystemHealthNotConfigured = "System health is not available: the Cloudflare KV settings (AUTOMATION_KV_NAMESPACE_ID, AUTOMATION_KV_API_TOKEN, CLOUDFLARE_ACCOUNT_ID) are not set for this environment."
	msgSystemHealthNoSnapshot    = "No health check has run yet. The stonesuite-watchdog Worker writes one every hour once it is deployed."
	msgSystemHealthReadFailed    = "Failed to load the latest health check."
)

// snapshotReader reads one value from the Cloudflare KV namespace the
// watchdog Worker writes to (automation.KVMirror).
type snapshotReader interface {
	Configured() bool
	Get(ctx context.Context, key string) ([]byte, bool, error)
}

// SystemHealthPlatformOps serves GET /api/platform/system-health: the latest
// snapshot the stonesuite-watchdog Worker saved (Neon usage per project, Fly
// machine state and crashes, awake time, website checks, open problems).
// Platform-admin only. It only reads Cloudflare KV, never Neon or Fly, so
// opening the page costs nothing and the numbers always match the emails.
type SystemHealthPlatformOps struct {
	cp platformAdminChecker
	kv snapshotReader
}

// NewSystemHealthPlatformOps wires the handler. kv may be a nil
// *automation.KVMirror (Configured() is false): the page then explains which
// settings are missing instead of failing.
func NewSystemHealthPlatformOps(cp platformAdminChecker, kv snapshotReader) *SystemHealthPlatformOps {
	return &SystemHealthPlatformOps{cp: cp, kv: kv}
}

type systemHealthResponse struct {
	Success    bool            `json:"success"`
	Configured bool            `json:"configured"`
	Snapshot   json.RawMessage `json:"snapshot"`
	Message    string          `json:"message,omitempty"`
}

// Get handles GET /api/platform/system-health.
func (h *SystemHealthPlatformOps) Get(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	isAdmin, err := h.cp.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !isAdmin {
		logSecurityEvent(r, "platform_system_health_denied", "identity", payload.ID)
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}

	resp := systemHealthResponse{Success: true, Snapshot: json.RawMessage("null")}
	if h.kv == nil || !h.kv.Configured() {
		resp.Message = msgSystemHealthNotConfigured
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Configured = true
	val, found, err := h.kv.Get(r.Context(), automation.WatchdogSnapshotKey)
	if err != nil {
		slog.Error("read watchdog snapshot from cloudflare kv failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusBadGateway, msgSystemHealthReadFailed)
		return
	}
	if !found {
		resp.Message = msgSystemHealthNoSnapshot
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !json.Valid(val) {
		slog.Error("watchdog snapshot in cloudflare kv is not valid json", "request_id", middleware.RequestIDFromContext(r.Context()), "bytes", len(val))
		fail(w, http.StatusBadGateway, msgSystemHealthReadFailed)
		return
	}
	resp.Snapshot = json.RawMessage(val)
	writeJSON(w, http.StatusOK, resp)
}
