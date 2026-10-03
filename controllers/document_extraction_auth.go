package controllers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// docExtractCaller is the authenticated caller of one request: the tenant pool
// and record, the JWT identity, and a store over that pool.
type docExtractCaller struct {
	pool       *pgxpool.Pool
	tenant     *tenancy.Tenant
	identityID string
	store      docExtractStore
}

// authenticate resolves the caller through h.identify and writes 401/500 on
// failure. It does not check a permission; callers follow with requireCreate.
func (h *DocExtractOps) authenticate(w http.ResponseWriter, r *http.Request) (*docExtractCaller, bool) {
	c, status, msg := h.identify(r)
	if status != 0 {
		fail(w, status, msg)
		return nil, false
	}
	return c, true
}

// identifyFromContext resolves the JWT identity, tenant record and tenant pool
// from request context (set by RequireAuth + the tenancy resolver), never from
// the body. A non-zero status is the HTTP failure to write.
func (h *DocExtractOps) identifyFromContext(r *http.Request) (*docExtractCaller, int, string) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		return nil, http.StatusUnauthorized, "Authentication required."
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		return nil, http.StatusInternalServerError, "Tenant database not resolved."
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		return nil, http.StatusInternalServerError, "Tenant not resolved."
	}
	return &docExtractCaller{pool: pool, tenant: tenant, identityID: payload.ID, store: h.newStore(pool)}, 0, ""
}

// checkFromPool is the default permission check: authz.Check on the tenant pool.
func checkFromPool(ctx context.Context, c *docExtractCaller, res authz.Resource, act authz.Action) (authz.Decision, error) {
	return authz.Check(ctx, c.pool, c.identityID, res, act)
}

// resourceForDocType maps a doc type to its RBAC resource. An unsupported or
// unknown type answers 400 and returns false.
func resourceForDocType(w http.ResponseWriter, docType docextract.DocType) (authz.Resource, bool) {
	if res, ok := docExtractResources[docType]; ok {
		return res, true
	}
	if _, known := docExtractUnsupported[docType]; known {
		fail(w, http.StatusBadRequest, "Creating a "+string(docType)+" from a document is not supported yet.")
		return "", false
	}
	fail(w, http.StatusBadRequest, "Unknown document type.")
	return "", false
}

// requireCreate enforces Require(<docType resource>, create): 400 for an
// unsupported doc type, 403 (after a permission_denied security log) on denial.
func (h *DocExtractOps) requireCreate(w http.ResponseWriter, r *http.Request, c *docExtractCaller, docType docextract.DocType) bool {
	resource, ok := resourceForDocType(w, docType)
	if !ok {
		return false
	}
	decision, err := h.check(r.Context(), c, resource, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied", "identity", c.identityID,
			"resource", string(resource), "action", string(authz.ActionCreate))
		fail(w, http.StatusForbidden, "You do not have permission to create this document.")
		return false
	}
	return true
}

// requireAI answers 403 {code: ai_disabled} unless the platform and tenant AI
// switches are both on. Extraction can use the LLM, so it is gated like the assistant.
func (h *DocExtractOps) requireAI(w http.ResponseWriter, r *http.Request, c *docExtractCaller) bool {
	ok, err := h.aiAvailable(r.Context(), c)
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: ai availability check failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to check assistant status.")
		return false
	}
	if !ok {
		failWithCode(w, http.StatusForbidden, "The AI assistant is turned off for your workspace.", codeAIDisabled)
		return false
	}
	return true
}

// loadOwned loads the caller's own extraction. Another owner's id is a 404 (the
// store scopes by owner in SQL), logged as doc_extract_idor when the id does
// exist for someone else. An expired row is already deleted by the store; its
// staging object is removed best-effort and the answer is 404 {code: expired}.
func (h *DocExtractOps) loadOwned(w http.ResponseWriter, r *http.Request, c *docExtractCaller, id string) (*docextractjob.Extraction, bool) {
	ex, err := c.store.Get(r.Context(), id, c.identityID)
	switch {
	case err == nil:
		return ex, true
	case errors.Is(err, docextractjob.ErrExpired):
		h.deleteStaging(r.Context(), c.tenant, ex)
		failWithCode(w, http.StatusNotFound, "This document expired. Please upload it again.", codeExpired)
	case errors.Is(err, docextractjob.ErrNotFound):
		if other, gerr := c.store.GetInternal(r.Context(), id); gerr == nil && other.OwnerIdentityID != c.identityID {
			logSecurityEvent(r, eventDocExtractIDOR, "identity", c.identityID, "extraction", id)
		}
		fail(w, http.StatusNotFound, "Document extraction not found.")
	default:
		slog.ErrorContext(r.Context(), "doc extract: load extraction", "extractionId", id, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load document extraction.")
	}
	return nil, false
}

// authOwned is the shared prologue of every {id} endpoint: flag, caller,
// owner-only load, then Require(<row doc type resource>, create) and, when
// needAI, the AI-availability gate.
func (h *DocExtractOps) authOwned(w http.ResponseWriter, r *http.Request, needAI bool) (*docExtractCaller, *docextractjob.Extraction, bool) {
	if !h.enabled(w) {
		return nil, nil, false
	}
	c, ok := h.authenticate(w, r)
	if !ok {
		return nil, nil, false
	}
	ex, ok := h.loadOwned(w, r, c, r.PathValue("id"))
	if !ok {
		return nil, nil, false
	}
	if !h.requireCreate(w, r, c, docextract.DocType(ex.DocType)) {
		return nil, nil, false
	}
	if needAI && !h.requireAI(w, r, c) {
		return nil, nil, false
	}
	return c, ex, true
}

// deleteStaging removes the extraction's staging object, best-effort: a
// failure is logged and never fails the request (the sweeper purges by TTL).
func (h *DocExtractOps) deleteStaging(ctx context.Context, tenant *tenancy.Tenant, ex *docextractjob.Extraction) {
	if ex == nil || ex.StagingKey == "" {
		return
	}
	objs := h.objectsFor(tenant)
	if objs == nil {
		return
	}
	if err := objs.Delete(ctx, ex.StagingKey); err != nil {
		slog.WarnContext(ctx, "doc extract: delete staging object", "extractionId", ex.ID, "error", err)
	}
}

// docExtractReadGate answers "may the caller read this record" for the records
// a result links to (duplicates, references), caching one grant per resource.
type docExtractReadGate struct {
	ctx        context.Context
	c          *docExtractCaller
	check      func(ctx context.Context, c *docExtractCaller, res authz.Resource, act authz.Action) (authz.Decision, error)
	scopeCache map[authz.Resource]authz.Decision
}

// newReadGate builds a gate for the caller of r.
func (h *DocExtractOps) newReadGate(r *http.Request, c *docExtractCaller) *docExtractReadGate {
	return &docExtractReadGate{ctx: r.Context(), c: c, check: h.check, scopeCache: map[authz.Resource]authz.Decision{}}
}

// canRead reports whether the caller holds a read grant on resource and, below
// `all` scope, owns the record (recordInScope). Any lookup error fails closed.
func (g *docExtractReadGate) canRead(resource authz.Resource, ownerUserID string) bool {
	d, ok := g.scopeCache[resource]
	if !ok {
		var err error
		if d, err = g.check(g.ctx, g.c, resource, authz.ActionRead); err != nil {
			d = authz.Decision{}
		}
		g.scopeCache[resource] = d
	}
	if !d.Allowed {
		return false
	}
	in, err := recordInScope(g.ctx, g.c.pool, d.Scope, g.c.identityID, ownerUserID)
	return err == nil && in
}
