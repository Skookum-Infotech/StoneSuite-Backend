package controllers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/usage"
)

// usageDateLayout is how period boundaries are written (calendar dates, UTC).
const usageDateLayout = "2006-01-02"

// tokenUsageReader reads a tenant's billing anchor and metered tokens (*usage.Store).
type tokenUsageReader interface {
	BillingAnchor(ctx context.Context, tenantID string) (time.Time, error)
	SumTokens(ctx context.Context, tenantID string, p usage.Period) (usage.TokenTotals, error)
}

// storageSnapshotter returns a tenant's bucket measurement (*usage.StorageRefresher).
type storageSnapshotter interface {
	Snapshot(ctx context.Context, tenantID, bucket string) (*usage.StorageSnapshot, error)
}

// subscriptionUsageEnv is what one request resolves: the caller's own tenant
// (from the JWT via TenantResolver — never from the request) and its RBAC check.
type subscriptionUsageEnv struct {
	tenantID string
	bucket   string
	check    func(ctx context.Context, identityID string) (authz.Decision, error)
}

// SubscriptionUsageOps serves GET /api/tenant/subscription/usage.
type SubscriptionUsageOps struct {
	tokens  tokenUsageReader
	storage storageSnapshotter
	now     func() time.Time
	resolve func(r *http.Request) (subscriptionUsageEnv, error)
}

// NewSubscriptionUsageOps builds the handler over the control-plane usage
// store and the storage refresher.
func NewSubscriptionUsageOps(tokens tokenUsageReader, storage storageSnapshotter) *SubscriptionUsageOps {
	return &SubscriptionUsageOps{tokens: tokens, storage: storage, now: time.Now,
		resolve: func(r *http.Request) (subscriptionUsageEnv, error) {
			tenant, err := tenancy.TenantFromContext(r.Context())
			if err != nil {
				return subscriptionUsageEnv{}, err
			}
			pool, err := tenancy.PoolFromContext(r.Context())
			if err != nil {
				return subscriptionUsageEnv{}, err
			}
			return subscriptionUsageEnv{tenantID: tenant.ID, bucket: tenant.R2Bucket,
				check: func(ctx context.Context, identityID string) (authz.Decision, error) {
					return authz.Check(ctx, pool, identityID, authz.ResourceSubscription, authz.ActionRead)
				}}, nil
		}}
}

// storageUsageJSON is the storage half of the response: the overall size of
// the tenant's bucket right now — not tied to any billing period. LimitBytes
// (an overall cap) is null until subscription plans carry limits.
type storageUsageJSON struct {
	UsedBytes   int64      `json:"usedBytes"`
	ObjectCount int64      `json:"objectCount"`
	MeasuredAt  *time.Time `json:"measuredAt"`
	LimitBytes  *int64     `json:"limitBytes"`
}

// sourceTokensJSON is one row of the per-source token breakdown.
type sourceTokensJSON struct {
	Source      string `json:"source"`
	TotalTokens int64  `json:"totalTokens"`
}

// aiTokenUsageJSON is the AI-token half of the response: tokens used in the
// current monthly billing period [PeriodStart, PeriodEnd), which resets at
// PeriodEnd. LimitTokens (a per-period cap) is null until plans carry limits.
type aiTokenUsageJSON struct {
	PeriodStart      string             `json:"periodStart"`
	PeriodEnd        string             `json:"periodEnd"`
	PromptTokens     int64              `json:"promptTokens"`
	CompletionTokens int64              `json:"completionTokens"`
	TotalTokens      int64              `json:"totalTokens"`
	LimitTokens      *int64             `json:"limitTokens"`
	BySource         []sourceTokensJSON `json:"bySource"`
}

// subscriptionUsageJSON is the whole usage payload.
type subscriptionUsageJSON struct {
	Storage  storageUsageJSON `json:"storage"`
	AITokens aiTokenUsageJSON `json:"aiTokens"`
}

// GetUsage handles GET /api/tenant/subscription/usage: the caller's own
// tenant's overall R2 storage and its LLM tokens for the current monthly
// billing period.
func (h *SubscriptionUsageOps) GetUsage(w http.ResponseWriter, r *http.Request) {
	env, ok := h.authorize(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	anchor, err := h.tokens.BillingAnchor(ctx, env.tenantID)
	if err != nil {
		slog.ErrorContext(ctx, "subscription usage: billing anchor", "tenant_id", env.tenantID, "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load usage.")
		return
	}
	period := usage.CurrentPeriod(anchor, h.now())
	totals, err := h.tokens.SumTokens(ctx, env.tenantID, period)
	if err != nil {
		slog.ErrorContext(ctx, "subscription usage: sum tokens", "tenant_id", env.tenantID, "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load usage.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "usage": subscriptionUsageJSON{
		Storage:  h.storageUsage(ctx, env),
		AITokens: tokenUsageJSON(totals, period),
	}})
}

// storageUsage degrades to "not measured" (zero, null measuredAt) when the
// snapshot cannot be read, so a storage hiccup doesn't hide the token figures.
func (h *SubscriptionUsageOps) storageUsage(ctx context.Context, env subscriptionUsageEnv) storageUsageJSON {
	out := storageUsageJSON{}
	snap, err := h.storage.Snapshot(ctx, env.tenantID, env.bucket)
	if err != nil {
		slog.ErrorContext(ctx, "subscription usage: storage snapshot", "tenant_id", env.tenantID, "err", err)
		return out
	}
	if snap != nil {
		measured := snap.MeasuredAt
		out.UsedBytes, out.ObjectCount, out.MeasuredAt = snap.UsedBytes, snap.ObjectCount, &measured
	}
	return out
}

func tokenUsageJSON(t usage.TokenTotals, p usage.Period) aiTokenUsageJSON {
	out := aiTokenUsageJSON{
		PeriodStart: p.Start.Format(usageDateLayout), PeriodEnd: p.End.Format(usageDateLayout),
		PromptTokens: t.PromptTokens, CompletionTokens: t.CompletionTokens,
		TotalTokens: t.PromptTokens + t.CompletionTokens, BySource: []sourceTokensJSON{},
	}
	for _, s := range t.BySource {
		out.BySource = append(out.BySource, sourceTokensJSON{Source: string(s.Source), TotalTokens: s.PromptTokens + s.CompletionTokens})
	}
	return out
}

func (h *SubscriptionUsageOps) authorize(w http.ResponseWriter, r *http.Request) (subscriptionUsageEnv, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return subscriptionUsageEnv{}, false
	}
	env, err := h.resolve(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return subscriptionUsageEnv{}, false
	}
	decision, err := env.check(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return subscriptionUsageEnv{}, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceSubscription), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to view subscription usage.")
		return subscriptionUsageEnv{}, false
	}
	return env, true
}
