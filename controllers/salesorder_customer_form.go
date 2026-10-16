package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/orderform"
	"stonesuite-backend/salesorder"
)

// The customer order form behind a Sales Order, seen from the tenant side. A
// Sales Order drafted from a customer's portal order keeps the form exactly as
// the customer filed it; staff read it here, and only a super admin changes it.

// CustomerOrderForm GET /api/tenant/sales-orders/{uuid}/customer-order-form
//
// Reading follows the Sales Order itself: sales_order:read and the caller's
// scope over that order, so anyone who can open the order can open its form.
// 404 when the order was not placed from the portal.
func (h *SalesOrderOps) CustomerOrderForm(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	form, err := orderform.ForSalesOrder(r.Context(), pool, uuid)
	if errors.Is(err, orderform.ErrNotFound) {
		fail(w, http.StatusNotFound, "This sales order has no customer order form.")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "sales order customer order form failed", "salesOrder", uuid, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load the customer order form.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "form": form})
}

// UpdateCustomerOrderForm PUT /api/tenant/sales-orders/{uuid}/customer-order-form
//
// Super admin only, on top of the usual sales_order:update and scope check: the
// form is the customer's own record, shown back to them in the portal, so a
// role that may edit the order is not enough to rewrite it. The body is the
// whole form, as the customer sends it.
//
// While the Sales Order is a Draft it is saved along with the form (keeping
// what staff did to it); after that only the form changes. "synced" in the
// reply says which.
func (h *SalesOrderOps) UpdateCustomerOrderForm(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	super, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		slog.ErrorContext(r.Context(), "customer order form: super admin check failed", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !super {
		logSecurityEvent(r, "customer_order_form_edit_denied", "identity", identityID, "record", uuid)
		fail(w, http.StatusForbidden, "Only a super admin can change a customer order form.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCreatedOrderBodyBytes)
	var in orderform.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	res, err := orderform.StaffUpdate(r.Context(), pool, uuid, in, orderform.StaffEditor{
		EmployeeID: resolveEmployeeID(r, identityID),
		Label:      staffEditorLabel(r, pool, identityID),
	}, time.Now().UTC())
	if err != nil {
		customerOrderFormFail(w, r, err)
		return
	}

	// The audit entry sits on the Sales Order, with its state before and after
	// (the order is unchanged when it had moved on and only the form was saved).
	after, gerr := salesorder.Get(r.Context(), pool, uuid)
	if gerr != nil {
		slog.WarnContext(r.Context(), "customer order form: audit skipped", "salesOrder", uuid, "error", gerr)
	} else {
		auditSOMeta(r, pool, identityID, "update", uuid, before, after, map[string]any{
			"source":              "staff_customer_order_form",
			"customerOrderFormId": res.Form.ID,
			"synced":              res.Synced,
			"createdKits":         len(res.CreatedKits),
			"createdItems":        len(res.CreatedItems),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "form": res.Form, "synced": res.Synced})
}

// customerOrderFormFail maps a failed staff change to a response. Staff get the
// real reason (unlike the customer's portal copy).
func customerOrderFormFail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *orderform.ValidationError
	switch {
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, invalid.Message)
	case errors.Is(err, orderform.ErrNotFound):
		fail(w, http.StatusNotFound, "This sales order has no customer order form.")
	case inventory.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		soFail(w, err, "Failed to update the customer order form.")
	}
}

// staffEditorLabel names the signed-in staff member for the memo: "Name (email)",
// the email alone when there is no name, "staff" when neither is known. The
// lookup is by the caller's own identity inside the tenant's database.
func staffEditorLabel(r *http.Request, pool *pgxpool.Pool, identityID string) string {
	var name, email string
	err := pool.QueryRow(r.Context(),
		`SELECT full_name, email FROM users WHERE identity_id = $1::uuid`, identityID).Scan(&name, &email)
	if err != nil {
		return "staff"
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	switch {
	case name != "" && email != "":
		return name + " (" + email + ")"
	case name != "":
		return name
	case email != "":
		return email
	}
	return "staff"
}
