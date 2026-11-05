package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalinbox"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

const (
	defaultApprovalInboxLimit = 25
	approvalInboxAllContext   = "approval_inbox_all"
)

// approvalInboxLister is the part of approvalinbox the handler needs; the fake in tests records
// what the handler asked for.
type approvalInboxLister interface {
	List(ctx context.Context, check approvalinbox.CheckFunc, c approvalinbox.Caller,
		p approvalinbox.Params, now time.Time) (approvalinbox.Page, error)
}

type poolInboxLister struct{ pool *pgxpool.Pool }

func (l poolInboxLister) List(ctx context.Context, check approvalinbox.CheckFunc, c approvalinbox.Caller,
	p approvalinbox.Params, now time.Time) (approvalinbox.Page, error) {
	return approvalinbox.List(ctx, l.pool, check, c, p, now)
}

// approvalInboxEnv is everything the handler needs about the caller, resolved per request.
type approvalInboxEnv struct {
	caller approvalinbox.Caller
	check  approvalinbox.CheckFunc
	lister approvalInboxLister
}

// ApprovalInboxOps serves GET /api/tenant/approvals/inbox: one keyset-paged list of the records
// waiting for the caller's sign-off across every approvalchain module. The endpoint takes no
// record id (no IDOR surface); each item links to a detail page that enforces scope itself.
type ApprovalInboxOps struct {
	resolve func(r *http.Request, identityID string) (approvalInboxEnv, error)
	now     func() time.Time
}

// NewApprovalInboxOps builds the handler set; the tenant pool comes from the request.
func NewApprovalInboxOps() *ApprovalInboxOps {
	return &ApprovalInboxOps{resolve: resolveApprovalInboxEnv, now: time.Now}
}

func resolveApprovalInboxEnv(r *http.Request, identityID string) (approvalInboxEnv, error) {
	ctx := r.Context()
	pool, err := tenancy.PoolFromContext(ctx)
	if err != nil {
		return approvalInboxEnv{}, fmt.Errorf("tenant pool: %w", err)
	}
	superAdmin, err := authz.IsSuperAdmin(ctx, pool, identityID)
	if err != nil {
		return approvalInboxEnv{}, fmt.Errorf("role check: %w", err)
	}
	check, err := approvalinbox.GrantsCheck(ctx, pool, identityID)
	if err != nil {
		return approvalInboxEnv{}, err
	}
	employeeID, _ := workflow.EmployeeIDByIdentity(ctx, pool, identityID)
	return approvalInboxEnv{
		caller: approvalinbox.Caller{IdentityID: identityID, EmployeeID: employeeID, IsSuperAdmin: superAdmin},
		check:  check, lister: poolInboxLister{pool: pool},
	}, nil
}

// approvalInboxRequest is the validated query string.
type approvalInboxRequest struct {
	params approvalinbox.Params
}

// parseApprovalInboxRequest validates scope, module, limit and cursor strictly; the message is
// safe to return to the client.
func parseApprovalInboxRequest(r *http.Request) (approvalInboxRequest, string) {
	q := r.URL.Query()
	req := approvalInboxRequest{params: approvalinbox.Params{Scope: approvalinbox.ScopeMine, Limit: defaultApprovalInboxLimit}}
	if s := q.Get("scope"); s != "" {
		req.params.Scope = approvalinbox.Scope(s)
		if !req.params.Scope.Valid() {
			return req, "scope must be mine or all."
		}
	}
	if m := q.Get("module"); m != "" {
		if _, ok := approvalinbox.MetaFor(m); !ok {
			return req, "module must be an approval workflow key."
		}
		req.params.Module = m
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			return req, "limit must be a positive integer."
		}
		req.params.Limit = min(n, query.MaxLimit)
	}
	if c := q.Get("cursor"); c != "" {
		cur, err := approvalinbox.DecodeCursor(c)
		if err != nil {
			return req, "Invalid cursor."
		}
		req.params.Cursor = &cur
	}
	return req, ""
}

// List handles GET /api/tenant/approvals/inbox?scope=mine|all&module=&limit=&cursor=.
func (h *ApprovalInboxOps) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	req, msg := parseApprovalInboxRequest(r)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	started := h.now()
	env, err := h.resolve(r, payload.ID)
	if err != nil {
		slog.Error("approval inbox resolve", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if req.params.Scope == approvalinbox.ScopeAll && !env.caller.IsSuperAdmin {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "context", approvalInboxAllContext, "scope", string(req.params.Scope))
		fail(w, http.StatusForbidden, "You do not have permission to view the all-approvals inbox.")
		return
	}
	page, err := env.lister.List(r.Context(), env.check, env.caller, req.params, started.UTC())
	if err != nil {
		slog.Error("approval inbox list", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load approvals.")
		return
	}
	slog.Debug("approval inbox", "modules", len(page.Modules), "items", len(page.Items),
		"scope", string(req.params.Scope), "duration_ms", h.now().Sub(started).Milliseconds())

	resp := map[string]any{
		"success": true, "scope": string(req.params.Scope), "total": page.Total,
		"modules": page.Modules, "items": page.Items, "hasMore": page.HasMore,
	}
	if page.HasMore {
		resp["nextCursor"] = page.NextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}
