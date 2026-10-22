package controllers

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

const (
	defaultDeliveryLimit = 25
	deliveryCursorSep    = "|"
)

// deliveryStatuses mirrors the CHECK constraint automation_deliveries_status_chk in tenant/schema.sql; keep in sync.
var deliveryStatuses = map[string]bool{
	"planned": true, "claimed": true, "sent": true, "skipped": true,
	"failed": true, "dead": true, "unknown": true,
}

var deliveryRuleKeyRE = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

var deliveryUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var errBadDeliveryCursor = errors.New("invalid cursor")

func encodeDeliveryCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + deliveryCursorSep + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeDeliveryCursor(s string) (*DeliveryCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errBadDeliveryCursor
	}
	ts, id, ok := strings.Cut(string(raw), deliveryCursorSep)
	if !ok {
		return nil, errBadDeliveryCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, errBadDeliveryCursor
	}
	if !deliveryUUIDRE.MatchString(id) {
		return nil, errBadDeliveryCursor
	}
	return &DeliveryCursor{CreatedAt: t, ID: id}, nil
}

// parseDeliveryFilter validates the query string strictly; the message is safe to return.
func parseDeliveryFilter(r *http.Request) (DeliveryFilter, string) {
	q := r.URL.Query()
	f := DeliveryFilter{Limit: defaultDeliveryLimit}
	if s := q.Get("status"); s != "" {
		if !deliveryStatuses[s] {
			return f, "Invalid status."
		}
		f.Status = s
	}
	if k := q.Get("ruleKey"); k != "" {
		if !deliveryRuleKeyRE.MatchString(k) {
			return f, "Invalid ruleKey."
		}
		f.RuleKey = k
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			return f, "limit must be a positive integer."
		}
		f.Limit = min(n, query.MaxLimit)
	}
	if c := q.Get("cursor"); c != "" {
		cur, err := decodeDeliveryCursor(c)
		if err != nil {
			return f, "Invalid cursor."
		}
		f.Cursor = cur
	}
	return f, ""
}

// automationDeliveryEnv resolves the per-request tenant dependencies.
type automationDeliveryEnv struct {
	store automationDeliveryStore
	check func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error)
}

// AutomationDeliveryOps serves GET /api/tenant/automation/deliveries.
type AutomationDeliveryOps struct {
	resolve func(r *http.Request) (automationDeliveryEnv, error)
}

// NewAutomationDeliveryOps builds the handler set backed by the resolved tenant pool.
func NewAutomationDeliveryOps() *AutomationDeliveryOps {
	return &AutomationDeliveryOps{resolve: func(r *http.Request) (automationDeliveryEnv, error) {
		pool, err := tenancy.PoolFromContext(r.Context())
		if err != nil {
			return automationDeliveryEnv{}, err
		}
		return automationDeliveryEnv{
			store: pgAutomationDeliveryStore{pool: pool},
			check: func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error) {
				return authz.Check(ctx, pool, identityID, authz.ResourceAutomation, action)
			},
		}, nil
	}}
}

// List handles GET /api/tenant/automation/deliveries.
func (h *AutomationDeliveryOps) List(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	env, err := h.resolve(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	decision, err := env.check(r.Context(), payload.ID, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceAutomation), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read automation deliveries.")
		return
	}
	filter, msg := parseDeliveryFilter(r)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	rows, next, err := env.store.List(r.Context(), filter)
	if err != nil {
		slog.Error("list automation deliveries", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load automation deliveries.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "deliveries": rows, "nextCursor": next})
}
