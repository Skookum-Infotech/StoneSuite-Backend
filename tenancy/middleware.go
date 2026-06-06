package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
)

type ctxKey string

const (
	tenantCtxKey     ctxKey = "tenant"
	tenantPoolCtxKey ctxKey = "tenantPool"
)

// Resolver wires the control plane and pool router into HTTP middleware.
type Resolver struct {
	cp     *ControlPlane
	router *Router
}

// NewResolver builds a Resolver from a control plane and router.
func NewResolver(cp *ControlPlane, router *Router) *Resolver {
	return &Resolver{cp: cp, router: router}
}

// Middleware resolves the authenticated request's tenant and attaches the
// tenant + its connection pool to the context. It MUST run after RequireAuth
// (which populates the tenant id from the JWT).
func (rs *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		payload, err := middleware.GetUserFromContext(r.Context())
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Authentication required.")
			return
		}
		if payload.TenantID == "" {
			writeErr(w, http.StatusForbidden, "Token is not scoped to a tenant.")
			return
		}

		tenant, err := rs.cp.TenantByID(r.Context(), payload.TenantID)
		if errors.Is(err, ErrTenantNotFound) {
			writeErr(w, http.StatusForbidden, "Tenant not found.")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Failed to resolve tenant.")
			return
		}

		if !tenant.Servable() {
			writeErr(w, http.StatusForbidden, tenantUnservableMessage(tenant))
			return
		}

		pool, err := rs.router.PoolFor(r.Context(), tenant)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Failed to connect to tenant database.")
			return
		}

		ctx := context.WithValue(r.Context(), tenantCtxKey, tenant)
		ctx = context.WithValue(ctx, tenantPoolCtxKey, pool)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TenantFromContext returns the resolved tenant for the request.
func TenantFromContext(ctx context.Context) (*Tenant, error) {
	t, ok := ctx.Value(tenantCtxKey).(*Tenant)
	if !ok || t == nil {
		return nil, errors.New("no tenant in context")
	}
	return t, nil
}

// PoolFromContext returns the resolved tenant database pool for the request.
func PoolFromContext(ctx context.Context) (*pgxpool.Pool, error) {
	p, ok := ctx.Value(tenantPoolCtxKey).(*pgxpool.Pool)
	if !ok || p == nil {
		return nil, errors.New("no tenant pool in context")
	}
	return p, nil
}

func tenantUnservableMessage(t *Tenant) string {
	switch {
	case t.Status == StatusSuspended:
		return "This workspace is suspended."
	case t.Status == StatusDeleted:
		return "This workspace has been deleted."
	case t.Status == StatusRejected:
		return "This onboarding application was not approved."
	case t.Status == StatusInvited, t.Status == StatusSubmitted:
		return "This workspace has not been activated yet."
	case t.MigrationStatus == MigrationFailed:
		return "This workspace is temporarily unavailable (maintenance)."
	case t.Status == StatusProvisioning, t.DBName == "":
		// Provisioning, or active-but-not-yet-provisioned (no DB).
		return "Your workspace is still being set up. Please try again shortly."
	default:
		return "This workspace is not available."
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: msg})
}
