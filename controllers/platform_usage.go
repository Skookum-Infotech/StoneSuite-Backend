package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/usage"
)

const (
	usageDefaultLimit   = 50
	usageMaxLimit       = tenancy.MaxTenantPageSize
	usageMaxBodyBytes   = 1 << 10
	usageDaysPerMonth   = 30
	usageAckSnoozeShort = 7

	msgUsageUnavailable = "Usage is not available for this tenant."
	msgUsageLoadFailed  = "Failed to load usage."
	msgUsageBadPeriod   = "period must be one of 7d, 30d, 90d."
	msgUsageBadLimit    = "limit must be an integer between 1 and 200."
	msgUsageBadBody     = "Body must be {} or {\"snoozeDays\": 7 | 30}."
	msgUsageNoAlert     = "Alert not found."
)

// uuidPattern matches the canonical textual UUID form, so a malformed path id is a 404 rather than a database error.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// platformUsageStore is the fleet usage store (usage.FleetStore) as the handlers use it.
type platformUsageStore interface {
	LatestRolledUpDay(ctx context.Context) (*time.Time, error)
	DailyValues(ctx context.Context, tenantIDs []string, from, to time.Time) ([]usage.DailyValue, error)
	TokensByDay(ctx context.Context, tenantIDs []string, from, to time.Time) ([]usage.DailyValue, error)
	TokensBySource(ctx context.Context, tenantID string, from, to time.Time) (map[string]int64, error)
	TableSizes(ctx context.Context, tenantID string) ([]usage.TableSize, *time.Time, error)
	SignInStats(ctx context.Context, tenantID string, from, to time.Time) (usage.SignInStats, error)
	Watchlist(ctx context.Context, now time.Time) ([]usage.Alert, error)
	TenantAlerts(ctx context.Context, tenantID string, since time.Time) ([]usage.Alert, error)
	TopSeverityByTenant(ctx context.Context, now time.Time) (map[string]string, error)
	AckAlert(ctx context.Context, alertID, actorIdentityID string, snoozeDays int, now time.Time) (bool, error)
}

// platformUsageTenants is the control-plane tenant registry as the handlers use it.
type platformUsageTenants interface {
	platformAdminChecker
	ListTenants(ctx context.Context) ([]tenancy.Tenant, error)
	ListTenantsPage(ctx context.Context, limit int, cursor string) ([]tenancy.Tenant, string, error)
	TenantByID(ctx context.Context, id string) (*tenancy.Tenant, error)
}

// PlatformUsageOps serves /api/platform/usage/*: cross-tenant resource usage for
// platform admins. RequireAuth + IsPlatformAdmin, no tenant chain: it reads the
// control plane only, never a tenant database.
type PlatformUsageOps struct {
	cp    platformUsageTenants
	store platformUsageStore
	now   func() time.Time
}

// NewPlatformUsageOps wires the handlers; now is injected for tests (time.Now in production).
func NewPlatformUsageOps(cp platformUsageTenants, store platformUsageStore, now func() time.Time) *PlatformUsageOps {
	return &PlatformUsageOps{cp: cp, store: store, now: now}
}

func (h *PlatformUsageOps) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) (middleware.UserContextPayload, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return payload, false
	}
	isAdmin, err := h.cp.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return payload, false
	}
	if !isAdmin {
		logSecurityEvent(r, "permission_denied", "identity", payload.ID, "resource", "platform_usage", "action", r.Method)
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return payload, false
	}
	return payload, true
}

func (h *PlatformUsageOps) failLoad(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.Error("platform usage: "+what+" failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
	fail(w, http.StatusInternalServerError, msgUsageLoadFailed)
}

// parsePeriodParam reads ?period (default 30d); it writes the 400 itself.
func parsePeriodParam(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = usage.PeriodDefault
	}
	days, ok := usage.ParsePeriod(period)
	if !ok {
		fail(w, http.StatusBadRequest, msgUsageBadPeriod)
		return "", 0, false
	}
	return period, days, true
}

// Watchlist handles GET /api/platform/usage/watchlist.
func (h *PlatformUsageOps) Watchlist(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(w, r); !ok {
		return
	}
	alerts, err := h.store.Watchlist(r.Context(), h.now())
	if err != nil {
		h.failLoad(w, r, "load watchlist", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool              `json:"success"`
		Alerts  []usage.AlertView `json:"alerts"`
	}{true, usage.AlertViews(alerts)})
}

type ackBody struct {
	SnoozeDays *int `json:"snoozeDays"`
}

// Ack handles POST /api/platform/usage/alerts/{alertId}/ack with {} or {"snoozeDays": 7|30}.
func (h *PlatformUsageOps) Ack(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.requirePlatformAdmin(w, r)
	if !ok {
		return
	}
	var body ackBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, usageMaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, msgUsageBadBody)
		return
	}
	snooze := 0
	if body.SnoozeDays != nil {
		snooze = *body.SnoozeDays
		if snooze != usageAckSnoozeShort && snooze != usageDaysPerMonth {
			fail(w, http.StatusBadRequest, msgUsageBadBody)
			return
		}
	}
	alertID := r.PathValue("alertId")
	if !uuidPattern.MatchString(alertID) {
		fail(w, http.StatusNotFound, msgUsageNoAlert)
		return
	}
	found, err := h.store.AckAlert(r.Context(), alertID, payload.ID, snooze, h.now())
	if err != nil {
		h.failLoad(w, r, "ack alert", err)
		return
	}
	if !found {
		fail(w, http.StatusNotFound, msgUsageNoAlert)
		return
	}
	slog.Info("usage alert acknowledged", "alert_id", alertID, "actor_identity_id", payload.ID,
		"snooze_days", snooze, "request_id", middleware.RequestIDFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// parseLimitParam reads ?limit (default 50, max 200); it writes the 400 itself.
func parseLimitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return usageDefaultLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > usageMaxLimit {
		fail(w, http.StatusBadRequest, msgUsageBadLimit)
		return 0, false
	}
	return n, true
}
