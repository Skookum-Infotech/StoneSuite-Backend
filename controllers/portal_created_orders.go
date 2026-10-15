package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
	"stonesuite-backend/orderform"
	"stonesuite-backend/portal"
	"stonesuite-backend/salesorder"
)

// maxCreatedOrderBodyBytes bounds an order request: twenty areas with their
// longest notes are well under it, a pasted novel is not.
const maxCreatedOrderBodyBytes = 512 << 10

// customerNotActiveMessage is what a customer whose account is on hold sees.
const customerNotActiveMessage = "Your account can't place or change orders right now. Please contact us."

// orderLockedMessage is what a customer sees when they try to change an order
// whose Sales Order staff have already moved on from Draft.
const orderLockedMessage = "This order is now being processed and can't be changed here. Please contact us."

// orderStockMessage is what a customer sees when a change would need material
// that is not free: staff, not the customer, can sort that out.
const orderStockMessage = "Some of the materials on this order aren't available right now. Please contact us."

// PortalCreatedOrderOps serves the customer portal's "Created Orders": the
// countertop order form a customer files, the list of what they have filed, and
// changing an order while its Sales Order is still a draft.
//
// There is no authz.Check here, deliberately, for the same reason as
// PortalDocumentOps: a portal login holds no roles. The session's customer id,
// resolved server-side, is the boundary — nothing about the customer, the owner
// or the Sales Order is read from the request body.
//
// Routes:
//
//	GET  /api/portal/created-orders — the customer's orders, newest first (keyset-paginated)
//	GET  /api/portal/created-orders/{id} — one order, with its areas
//	POST /api/portal/created-orders — place an order; also drafts a Sales Order for staff
//	PUT  /api/portal/created-orders/{id} — change an order while its Sales Order is a draft;
//	     the draft follows, keeping what staff have added to it
type PortalCreatedOrderOps struct{}

// NewPortalCreatedOrderOps constructs the handler group.
func NewPortalCreatedOrderOps() *PortalCreatedOrderOps { return &PortalCreatedOrderOps{} }

// List GET /api/portal/created-orders
func (h *PortalCreatedOrderOps) List(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	page, err := orderform.List(r.Context(), pool, sess.CustomerID, portalListRequest(r))
	if err != nil {
		portalFail(w, err, "Failed to list your orders.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "orders": page.Orders, "nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Get GET /api/portal/created-orders/{id}
func (h *PortalCreatedOrderOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	order, err := orderform.Get(r.Context(), pool, sess.CustomerID, r.PathValue("id"))
	if errors.Is(err, orderform.ErrNotFound) {
		// 404 for another customer's order too: ids must not be probeable.
		fail(w, http.StatusNotFound, "Order not found.")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "portal created order get failed", "customer", sess.CustomerID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load your order.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "order": order})
}

// Create POST /api/portal/created-orders
func (h *PortalCreatedOrderOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	identityID, err := portalIdentityID(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCreatedOrderBodyBytes)
	var in orderform.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	res, err := orderform.Create(r.Context(), pool, submitterOf(sess), in, time.Now().UTC())
	if err != nil {
		status, message, serverFault := createdOrderErrorResponse(err)
		if serverFault {
			slog.ErrorContext(r.Context(), "portal created order failed",
				"customer", sess.CustomerID, "error", err)
		}
		fail(w, status, message)
		return
	}

	auditPortalOrder(r, pool, identityID, "create", res)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "order": res.Form})
}

// Update PUT /api/portal/created-orders/{id}
func (h *PortalCreatedOrderOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}
	identityID, err := portalIdentityID(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCreatedOrderBodyBytes)
	var in orderform.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	res, err := orderform.Update(r.Context(), pool, submitterOf(sess), r.PathValue("id"), in, time.Now().UTC())
	if err != nil {
		status, message, serverFault := createdOrderErrorResponse(err)
		if serverFault {
			slog.ErrorContext(r.Context(), "portal created order update failed",
				"customer", sess.CustomerID, "error", err)
		}
		fail(w, status, message)
		return
	}

	auditPortalOrder(r, pool, identityID, "update", res)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "order": res.Form})
}

// auditPortalOrder records the Sales Order a portal order created or changed,
// with where it came from. The audit trail (and the AI index that follows it)
// is on the Sales Order, the record staff work from.
func auditPortalOrder(r *http.Request, pool *pgxpool.Pool, identityID, action string, res *orderform.Result) {
	order, err := salesorder.Get(r.Context(), pool, res.SalesOrderUUID)
	if err != nil {
		slog.WarnContext(r.Context(), "portal created order: audit skipped",
			"salesOrder", res.SalesOrderUUID, "error", err)
		return
	}
	auditSOMeta(r, pool, identityID, action, order.ID, nil, order, map[string]any{
		"source":              "customer_portal",
		"customerOrderFormId": res.Form.ID,
		"createdKits":         len(res.CreatedKits),
		"createdItems":        len(res.CreatedItems),
	})
}

// submitterOf is the portal login the session resolved to: never read from the
// request body.
func submitterOf(sess *portal.Session) orderform.Submitter {
	return orderform.Submitter{
		PortalUserID: sess.PortalUserID,
		CustomerID:   sess.CustomerID,
		CustomerUUID: sess.CustomerUUID,
		FullName:     sess.FullName,
		Email:        sess.Email,
	}
}

// createdOrderErrorResponse maps a failure of placing or changing an order to the
// status and message the customer gets. serverFault is true when it is ours to
// log.
func createdOrderErrorResponse(err error) (status int, message string, serverFault bool) {
	var invalid *orderform.ValidationError
	var shortage *inventory.StockShortageError
	switch {
	case errors.As(err, &invalid):
		return http.StatusBadRequest, invalid.Message, false
	case errors.Is(err, orderform.ErrNotFound):
		// 404 for another customer's order too: ids must not be probeable.
		return http.StatusNotFound, "Order not found.", false
	case errors.Is(err, orderform.ErrNotEditable):
		return http.StatusConflict, orderLockedMessage, false
	case errors.Is(err, orderform.ErrCustomerNotActive):
		return http.StatusConflict, customerNotActiveMessage, false
	case errors.As(err, &shortage):
		return http.StatusConflict, orderStockMessage, false
	case salesorder.IsClientError(err), inventory.IsClientError(err):
		return http.StatusBadRequest, err.Error(), false
	default:
		return http.StatusInternalServerError, "We could not save your order. Please try again.", true
	}
}
