package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
	"stonesuite-backend/payment"
	"stonesuite-backend/portal"
	"stonesuite-backend/refund"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// PortalMessageOps is the staff side of a document's customer conversation.
//
// Without this, a customer can raise a query on an invoice and nobody ever sees
// it. Access follows the DOCUMENT, not a separate permission: if you may read
// the invoice you may read its thread, and if you may update the invoice you
// may reply. That keeps one rule to reason about instead of two that can drift.
type PortalMessageOps struct{}

// NewPortalMessageOps constructs the handler group.
func NewPortalMessageOps() *PortalMessageOps { return &PortalMessageOps{} }

// moduleResource maps a portal module to the RBAC resource that governs it.
//
// Returns false for anything not portal-visible, so the {module} path segment
// can never widen past the four modules the portal exposes.
func moduleResource(m portal.Module) (authz.Resource, bool) {
	switch m {
	case portal.ModuleSalesOrder:
		return authz.ResourceSalesOrder, true
	case portal.ModuleInvoice:
		return authz.ResourceInvoice, true
	case portal.ModulePayment:
		return authz.ResourcePayment, true
	case portal.ModuleRefund:
		return authz.ResourceRefund, true
	default:
		return "", false
	}
}

// documentOwner returns the document's owning users.id, for the scope check.
//
// Loads through each module's ordinary Get — the internal read, not PortalGet —
// because staff are not restricted to portal-visible states: someone handling a
// customer query about a draft must still be able to see the thread.
func documentOwner(r *http.Request, pool *pgxpool.Pool, m portal.Module, uuid string) (string, error) {
	switch m {
	case portal.ModuleSalesOrder:
		rec, err := salesorder.Get(r.Context(), pool, uuid)
		if err != nil {
			return "", err
		}
		return rec.OwnerUserID, nil
	case portal.ModuleInvoice:
		rec, err := invoice.Get(r.Context(), pool, uuid)
		if err != nil {
			return "", err
		}
		return rec.OwnerUserID, nil
	case portal.ModulePayment:
		rec, err := payment.Get(r.Context(), pool, uuid)
		if err != nil {
			return "", err
		}
		return rec.OwnerUserID, nil
	case portal.ModuleRefund:
		rec, err := refund.Get(r.Context(), pool, uuid)
		if err != nil {
			return "", err
		}
		return rec.OwnerUserID, nil
	default:
		return "", errors.New("unknown module")
	}
}

// authDocumentThread resolves the caller, the module and the document, applying
// the document's own permission and ownership scope.
//
// Returns the document's customer id, which is what scopes the thread. Scope
// denial is 404, not 403, matching every other single-record path.
func (h *PortalMessageOps) authDocumentThread(w http.ResponseWriter, r *http.Request,
	module portal.Module, action authz.Action) (*pgxpool.Pool, string, portal.Module, int, bool) {
	var zero portal.Module

	resource, ok := moduleResource(module)
	if !ok {
		fail(w, http.StatusNotFound, "Unknown document type.")
		return nil, "", zero, 0, false
	}

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", zero, 0, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", zero, 0, false
	}

	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", zero, 0, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied", "identity", payload.ID,
			"resource", string(resource), "action", string(action))
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" "+string(resource)+".")
		return nil, "", zero, 0, false
	}

	uuid := r.PathValue("uuid")
	if decision.Scope != authz.ScopeAll {
		ownerUserID, oerr := documentOwner(r, pool, module, uuid)
		if oerr != nil {
			fail(w, http.StatusNotFound, "Document not found.")
			return nil, "", zero, 0, false
		}
		allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, payload.ID, ownerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", zero, 0, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied", "identity", payload.ID, "record", uuid,
				"resource", string(resource), "action", string(action), "scope", string(decision.Scope))
			fail(w, http.StatusNotFound, "Document not found.")
			return nil, "", zero, 0, false
		}
	}

	customerID, err := portal.DocumentCustomerID(r.Context(), pool, module, uuid)
	if errors.Is(err, portal.ErrMessageDocumentNotVisible) {
		fail(w, http.StatusNotFound, "Document not found.")
		return nil, "", zero, 0, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load document.")
		return nil, "", zero, 0, false
	}
	return pool, payload.ID, module, customerID, true
}

// MessagesFor serves GET and POST on /api/tenant/{documents}/{uuid}/portal-messages.
//
// GET requires <resource>:read, POST requires <resource>:update — replying is a
// change to the customer-facing record of the document, not a read of it.
//
// The module is bound at registration, not read from a path wildcard: see
// PortalDocumentOps.MessagesFor for why a wildcard segment is unsafe here.
func (h *PortalMessageOps) MessagesFor(module portal.Module) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.messages(w, r, module)
	}
}

func (h *PortalMessageOps) messages(w http.ResponseWriter, r *http.Request, module portal.Module) {
	switch r.Method {
	case http.MethodGet:
		pool, _, module, customerID, ok := h.authDocumentThread(w, r, module, authz.ActionRead)
		if !ok {
			return
		}
		msgs, err := portal.ListMessages(r.Context(), pool, customerID, module, r.PathValue("uuid"))
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load messages.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "messages": msgs})

	case http.MethodPost:
		pool, identityID, module, customerID, ok := h.authDocumentThread(w, r, module, authz.ActionUpdate)
		if !ok {
			return
		}
		// A staff message must be attributable: portal_message requires an
		// employee id for author_kind='staff'. An admin identity with no
		// employee row cannot reply, and should be told why rather than
		// hitting a constraint violation.
		empID, found := workflow.EmployeeIDByIdentity(r.Context(), pool, identityID)
		if !found {
			fail(w, http.StatusForbidden,
				"Replying requires an employee profile linked to your account.")
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
		msg, err := portal.CreateStaffMessage(r.Context(), pool, customerID, empID,
			module, r.PathValue("uuid"), body.Body)
		var berr portal.BodyError
		if errors.As(err, &berr) {
			fail(w, http.StatusBadRequest, berr.Msg)
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to post reply.")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"success": true, "message": msg})

	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
