package authz

import (
	"context"
	"encoding/json"
	"net/http"

	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
)

type ctxKey string

const scopeCtxKey ctxKey = "authzScope"

// Require builds middleware that allows the request only if the authenticated
// caller holds {resource, action} in the resolved tenant. The granted scope is
// stashed in context for handlers to narrow row visibility (all|team|own).
//
// It MUST run after middleware.RequireAuth and tenancy Resolver.Middleware,
// which populate the identity and the tenant DB pool respectively.
func Require(resource Resource, action Action) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload, err := middleware.GetUserFromContext(r.Context())
			if err != nil || payload.ID == "" {
				deny(w, http.StatusUnauthorized, "Authentication required.")
				return
			}
			pool, err := tenancy.PoolFromContext(r.Context())
			if err != nil {
				deny(w, http.StatusInternalServerError, "Tenant database not resolved.")
				return
			}

			decision, err := Check(r.Context(), pool, payload.ID, resource, action)
			if err != nil {
				deny(w, http.StatusInternalServerError, "Permission check failed.")
				return
			}
			if !decision.Allowed {
				deny(w, http.StatusForbidden, "You do not have permission to "+string(action)+" "+string(resource)+".")
				return
			}

			ctx := context.WithValue(r.Context(), scopeCtxKey, decision.Scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ScopeFromContext returns the scope granted by the most recent Require check.
// Defaults to ScopeOwn (most restrictive) when absent.
func ScopeFromContext(ctx context.Context) Scope {
	if s, ok := ctx.Value(scopeCtxKey).(Scope); ok && s != "" {
		return s
	}
	return ScopeOwn
}

func deny(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: msg})
}
