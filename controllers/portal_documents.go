package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
	"stonesuite-backend/payment"
	"stonesuite-backend/portal"
	"stonesuite-backend/query"
	"stonesuite-backend/refund"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/tenancy"
)

// PortalDocumentOps serves the customer-facing document read surface.
//
// There is no authz.Check anywhere in this file, and that is deliberate. A
// portal customer holds no roles and no grants by construction — they have no
// `users` row, so authz.EffectiveGrants resolves to nothing. Authorization here
// is entirely the session's customer id, resolved server-side and passed to the
// store as a parameter. See portal.ResolveSession and each module's
// portal_search.go for why that is a boundary rather than a filter.
type PortalDocumentOps struct{}

// NewPortalDocumentOps constructs the handler group.
func NewPortalDocumentOps() *PortalDocumentOps { return &PortalDocumentOps{} }

// portalSession resolves the calling customer for this request.
//
// Every failure is 401 with the same message: a portal user whose access was
// revoked mid-session, or whose customer record was demoted out of CUST stage,
// learns only that they must sign in again.
func portalSession(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, *portal.Session, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, false
	}
	sess, err := portal.ResolveSession(r.Context(), pool, payload.ID)
	if errors.Is(err, portal.ErrPortalUserNotFound) {
		logSecurityEvent(r, "portal_session_unresolved",
			"identity", payload.ID, "tenant", payload.TenantID)
		fail(w, http.StatusUnauthorized, "Your portal access is not available.")
		return nil, nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve portal session.")
		return nil, nil, false
	}
	return pool, sess, true
}

// portalIdentityID returns the calling identity id from the request context.
func portalIdentityID(r *http.Request) (string, error) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		return "", errors.New("no authenticated identity")
	}
	return payload.ID, nil
}

// portalListRequest reads paging options from the query string (GET list).
func portalListRequest(r *http.Request) query.Request {
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	return req
}

// portalSearchRequest reads the full filter/sort payload (POST search).
//
// A customer_id filter in this body is harmless: query.Build ANDs it onto the
// session's customer predicate, so it can only narrow the caller's own records.
func portalSearchRequest(w http.ResponseWriter, r *http.Request) (query.Request, bool) {
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return req, false
		}
	}
	return req, true
}

// portalPage writes a list response.
func portalPage(w http.ResponseWriter, records any, nextCursor string, hasMore bool) {
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "records": records,
		"nextCursor": nextCursor, "hasMore": hasMore,
	})
}

// portalFail maps store errors to status codes. An invalid filter key is the
// caller's fault (400); a missing or out-of-scope document is 404, never 403.
func portalFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	if errors.As(err, &invalid) {
		fail(w, http.StatusBadRequest, invalid.Error())
		return
	}
	fail(w, http.StatusInternalServerError, fallback)
}

// ---- sales orders -----------------------------------------------------------

func (h *PortalDocumentOps) ListSalesOrders(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	page, err := salesorder.PortalSearch(r.Context(), pool, sess.CustomerID, portalListRequest(r))
	if err != nil {
		portalFail(w, err, "Failed to list sales orders.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) SearchSalesOrders(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	req, ok := portalSearchRequest(w, r)
	if !ok {
		return
	}
	page, err := salesorder.PortalSearch(r.Context(), pool, sess.CustomerID, req)
	if err != nil {
		portalFail(w, err, "Failed to search sales orders.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) GetSalesOrder(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	rec, err := salesorder.PortalGet(r.Context(), pool, sess.CustomerID, r.PathValue("uuid"))
	if errors.Is(err, salesorder.ErrNotFound) {
		fail(w, http.StatusNotFound, "Sales order not found.")
		return
	}
	if err != nil {
		portalFail(w, err, "Failed to load sales order.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

// ---- invoices ---------------------------------------------------------------

func (h *PortalDocumentOps) ListInvoices(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	page, err := invoice.PortalSearch(r.Context(), pool, sess.CustomerID, portalListRequest(r))
	if err != nil {
		portalFail(w, err, "Failed to list invoices.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) SearchInvoices(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	req, ok := portalSearchRequest(w, r)
	if !ok {
		return
	}
	page, err := invoice.PortalSearch(r.Context(), pool, sess.CustomerID, req)
	if err != nil {
		portalFail(w, err, "Failed to search invoices.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) GetInvoice(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	rec, err := invoice.PortalGet(r.Context(), pool, sess.CustomerID, r.PathValue("uuid"))
	if errors.Is(err, invoice.ErrNotFound) {
		fail(w, http.StatusNotFound, "Invoice not found.")
		return
	}
	if err != nil {
		portalFail(w, err, "Failed to load invoice.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

// ---- payments ---------------------------------------------------------------

func (h *PortalDocumentOps) ListPayments(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	page, err := payment.PortalSearch(r.Context(), pool, sess.CustomerID, portalListRequest(r))
	if err != nil {
		portalFail(w, err, "Failed to list payments.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) SearchPayments(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	req, ok := portalSearchRequest(w, r)
	if !ok {
		return
	}
	page, err := payment.PortalSearch(r.Context(), pool, sess.CustomerID, req)
	if err != nil {
		portalFail(w, err, "Failed to search payments.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) GetPayment(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	rec, err := payment.PortalGet(r.Context(), pool, sess.CustomerID, r.PathValue("uuid"))
	if errors.Is(err, payment.ErrNotFound) {
		fail(w, http.StatusNotFound, "Payment not found.")
		return
	}
	if err != nil {
		portalFail(w, err, "Failed to load payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

// ---- refunds ----------------------------------------------------------------

func (h *PortalDocumentOps) ListRefunds(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	page, err := refund.PortalSearch(r.Context(), pool, sess.CustomerID, portalListRequest(r))
	if err != nil {
		portalFail(w, err, "Failed to list refunds.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) SearchRefunds(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	req, ok := portalSearchRequest(w, r)
	if !ok {
		return
	}
	page, err := refund.PortalSearch(r.Context(), pool, sess.CustomerID, req)
	if err != nil {
		portalFail(w, err, "Failed to search refunds.")
		return
	}
	portalPage(w, page.Records, page.NextCursor, page.HasMore)
}

func (h *PortalDocumentOps) GetRefund(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	rec, err := refund.PortalGet(r.Context(), pool, sess.CustomerID, r.PathValue("uuid"))
	if errors.Is(err, refund.ErrNotFound) {
		fail(w, http.StatusNotFound, "Refund not found.")
		return
	}
	if err != nil {
		portalFail(w, err, "Failed to load refund.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

// ---- messages ---------------------------------------------------------------

// documentVisible confirms the document exists, belongs to the caller's
// customer, and is in a portal-visible state — by loading it through the very
// same PortalGet the read endpoints use, so the message endpoints can never
// reach a document the read endpoints would hide.
func documentVisible(r *http.Request, pool *pgxpool.Pool, customerID int, m portal.Module, uuid string) (bool, error) {
	switch m {
	case portal.ModuleSalesOrder:
		_, err := salesorder.PortalGet(r.Context(), pool, customerID, uuid)
		return err == nil, ignoreNotFound(err, salesorder.ErrNotFound)
	case portal.ModuleInvoice:
		_, err := invoice.PortalGet(r.Context(), pool, customerID, uuid)
		return err == nil, ignoreNotFound(err, invoice.ErrNotFound)
	case portal.ModulePayment:
		_, err := payment.PortalGet(r.Context(), pool, customerID, uuid)
		return err == nil, ignoreNotFound(err, payment.ErrNotFound)
	case portal.ModuleRefund:
		_, err := refund.PortalGet(r.Context(), pool, customerID, uuid)
		return err == nil, ignoreNotFound(err, refund.ErrNotFound)
	default:
		return false, nil
	}
}

// ignoreNotFound collapses a module's not-found sentinel to nil so the caller
// can distinguish "absent" (false, nil) from "broken" (false, err).
func ignoreNotFound(err, notFound error) error {
	if err == nil || errors.Is(err, notFound) {
		return nil
	}
	return err
}

// MessagesFor serves GET and POST on /api/portal/{documents}/{uuid}/messages.
//
// The module is bound at registration rather than read from a path wildcard.
// A wildcard segment here collides with /api/portal/auth/... — both are five
// segments, neither is more specific, and net/http's ServeMux panics at
// registration on exactly that ambiguity. Binding per route also means the
// message paths match the document paths they hang off.
func (h *PortalDocumentOps) MessagesFor(module portal.Module) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.messages(w, r, module)
	}
}

func (h *PortalDocumentOps) messages(w http.ResponseWriter, r *http.Request, module portal.Module) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	uuid := r.PathValue("uuid")

	visible, err := documentVisible(r, pool, sess.CustomerID, module, uuid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load document.")
		return
	}
	if !visible {
		// 404 rather than 403: the message endpoint must not reveal that a
		// document id exists under another customer.
		logSecurityEvent(r, "portal_message_document_denied",
			"customer", sess.CustomerID, "module", string(module), "document", uuid)
		fail(w, http.StatusNotFound, "Document not found.")
		return
	}

	switch r.Method {
	case http.MethodGet:
		msgs, err := portal.ListMessages(r.Context(), pool, sess.CustomerID, module, uuid)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load messages.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "messages": msgs})
	case http.MethodPost:
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
		msg, err := portal.CreatePortalMessage(r.Context(), pool, sess.CustomerID,
			sess.PortalUserID, module, uuid, body.Body)
		var berr portal.BodyError
		if errors.As(err, &berr) {
			fail(w, http.StatusBadRequest, berr.Msg)
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to post message.")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"success": true, "message": msg})
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
