package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/automation"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// automationPreviewer is the consumer-side surface the preview handler needs.
type automationPreviewer interface {
	Preview(ctx context.Context, ruleKey string) (automation.Preview, error)
}

// automationPreviewEnv resolves the per-request tenant dependencies.
type automationPreviewEnv struct {
	previewer automationPreviewer
	check     func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error)
}

// AutomationPreviewOps serves POST /api/tenant/automation/rules/{key}/preview.
type AutomationPreviewOps struct {
	resolve func(r *http.Request) (automationPreviewEnv, error)
}

// pgAutomationPreviewer previews against the tenant pool with a Runner-equivalent JobEnv.
type pgAutomationPreviewer struct {
	tenant   *tenancy.Tenant
	pool     *pgxpool.Pool
	fallback *time.Location
}

// NewAutomationPreviewOps builds the handler; fallback is the tenant-timezone fallback the Runner uses.
func NewAutomationPreviewOps(fallback *time.Location) *AutomationPreviewOps {
	return &AutomationPreviewOps{resolve: func(r *http.Request) (automationPreviewEnv, error) {
		pool, err := tenancy.PoolFromContext(r.Context())
		if err != nil {
			return automationPreviewEnv{}, err
		}
		t, err := tenancy.TenantFromContext(r.Context())
		if err != nil {
			return automationPreviewEnv{}, err
		}
		return automationPreviewEnv{
			previewer: pgAutomationPreviewer{tenant: t, pool: pool, fallback: fallback},
			check: func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error) {
				return authz.Check(ctx, pool, identityID, authz.ResourceAutomation, action)
			},
		}, nil
	}}
}

// Preview computes the dry run for the tenant at the current time.
func (p pgAutomationPreviewer) Preview(ctx context.Context, ruleKey string) (automation.Preview, error) {
	profile, err := companyprofile.Get(ctx, p.pool)
	if err != nil {
		return automation.Preview{}, fmt.Errorf("load company profile: %w", err)
	}
	env := automation.NewPreviewEnv(p.tenant, time.Now(), profile.Timezone, p.fallback)
	return automation.PreviewAR(ctx, p.pool, env, ruleKey)
}

// Preview handles POST /api/tenant/automation/rules/{key}/preview.
func (h *AutomationPreviewOps) Preview(w http.ResponseWriter, r *http.Request) {
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
		fail(w, http.StatusForbidden, "You do not have permission to read automation rules.")
		return
	}
	key := r.PathValue("key")
	res, err := env.previewer.Preview(r.Context(), key)
	switch {
	case errors.Is(err, automation.ErrRuleNotFound):
		fail(w, http.StatusNotFound, "Automation rule not found.")
		return
	case errors.Is(err, automation.ErrPreviewUnsupported):
		fail(w, http.StatusBadRequest, "Preview not supported for this rule.")
		return
	case err != nil:
		slog.Error("preview automation rule", "rule", key, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to preview automation rule.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "counts": res.Counts, "recipients": res.Recipients})
}
