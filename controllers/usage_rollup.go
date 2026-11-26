package controllers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/usage"
)

// Usage rollup request limits.
const (
	// maxUsageRollupBodyBytes caps the signed rollup body (the Worker sends "{}").
	maxUsageRollupBodyBytes = 4 << 10
	// usageRollupMaxBackfillDays is how far back an explicit day may reach.
	usageRollupMaxBackfillDays = 31
	// usageRollupDayLayout is the wire format of the optional "day" field.
	usageRollupDayLayout = "2006-01-02"
)

// usageRoller computes one UTC day's per-tenant usage rollup (*usage.Rollup).
type usageRoller interface {
	Run(ctx context.Context, day time.Time) (usage.RollupSummary, error)
}

// UsageRollupOps serves POST /api/internal/usage/rollup — the machine-only
// entry point the daily Cloudflare Cron Trigger calls to roll up the previous
// UTC day's per-tenant usage. Deliberately NOT gated by the platform
// automation switch: metering must run while automation is off.
type UsageRollupOps struct {
	roller  usageRoller
	secret  string
	replays *middleware.ReplayGuard
	now     func() time.Time
}

// NewUsageRollupOps builds the handler. An empty secret disables the route.
func NewUsageRollupOps(roller usageRoller, secret string) *UsageRollupOps {
	return &UsageRollupOps{roller: roller, secret: secret, replays: middleware.NewReplayGuard(), now: time.Now}
}

type usageRollupRequest struct {
	Day string `json:"day"`
}

type usageRollupResponse struct {
	Success bool                `json:"success"`
	Day     string              `json:"day"`
	Summary usage.RollupSummary `json:"summary"`
}

// Rollup handles POST /api/internal/usage/rollup. No JWT and no TenantResolver:
// the caller is not a user and the work spans tenants, so the HMAC signature
// over timestamp+body is the only credential. The work runs synchronously so
// Fly keeps the machine up until it finishes.
func (h *UsageRollupOps) Rollup(w http.ResponseWriter, r *http.Request) {
	if h.secret == "" {
		writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "API route not found."})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUsageRollupBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body."})
		return
	}
	sig := r.Header.Get(middleware.InternalSignatureHeader)
	if err := middleware.VerifyInternalSignature(h.secret, r.Method, r.URL.Path,
		r.Header.Get(middleware.InternalTimestampHeader), sig, body, h.now()); err != nil {
		logSecurityEvent(r, "usage_rollup_bad_signature", "reason", err.Error())
		writeJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized."})
		return
	}
	if !h.replays.FirstUse(sig, h.now()) {
		logSecurityEvent(r, "usage_rollup_replay")
		writeJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized."})
		return
	}

	day, msg := h.resolveDay(body)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: msg})
		return
	}

	started := h.now()
	sum, err := h.roller.Run(r.Context(), day)
	if err != nil {
		slog.Error("usage rollup failed", "day", day.Format(usageRollupDayLayout), "err", err)
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Usage rollup failed."})
		return
	}
	slog.Info("usage rollup complete", "day", day.Format(usageRollupDayLayout), "tenants", sum.Tenants,
		"failed", sum.Failed, "duration_ms", time.Since(started).Milliseconds())
	writeJSON(w, http.StatusOK, usageRollupResponse{Success: true, Day: day.Format(usageRollupDayLayout), Summary: sum})
}

// resolveDay returns the UTC day to roll up from the request body: yesterday by
// default, or the explicit "day" when it is a past UTC day within the backfill
// window. A non-empty message means the request is invalid.
func (h *UsageRollupOps) resolveDay(body []byte) (time.Time, string) {
	n := h.now().UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)

	var req usageRollupRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return time.Time{}, "Invalid request body."
		}
	}
	if req.Day == "" {
		return today.AddDate(0, 0, -1), ""
	}
	day, err := time.Parse(usageRollupDayLayout, req.Day)
	if err != nil {
		return time.Time{}, "day must be a date in yyyy-mm-dd format."
	}
	if !day.Before(today) {
		return time.Time{}, "day must be a past UTC day."
	}
	if day.Before(today.AddDate(0, 0, -usageRollupMaxBackfillDays)) {
		return time.Time{}, "day is older than the 31-day backfill window."
	}
	return day, ""
}
