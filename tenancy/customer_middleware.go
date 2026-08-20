package tenancy

import (
	"context"
	"errors"
	"net/http"

	"stonesuite-backend/middleware"
)

// CustomerMiddleware resolves the authenticated customer request's tenant
// and attaches the tenant + its connection pool to the context, exactly like
// Middleware does for staff — so every downstream helper (TenantFromContext,
// PoolFromContext) works unchanged regardless of which principal made the
// request. It MUST run after middleware.RequireCustomerAuth.
func (rs *Resolver) CustomerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		payload, err := middleware.GetCustomerFromContext(r.Context())
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
