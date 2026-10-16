package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/inventory"
	"stonesuite-backend/orderform"
	"stonesuite-backend/salesorder"
)

// Both customer-order-form handlers sit behind the Sales Order's own auth: an
// unauthenticated call is a 401, never a 5xx, a panic or an open door.
func TestCustomerOrderFormHandlers_RequireAuth(t *testing.T) {
	ops := NewSalesOrderOps(nil)
	handlers := map[string]struct {
		method string
		fn     http.HandlerFunc
	}{
		"read":   {http.MethodGet, ops.CustomerOrderForm},
		"change": {http.MethodPut, ops.UpdateCustomerOrderForm},
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(h.method, "/customer-order-form", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()

			h.fn(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestCustomerOrderFormFail_MapsEachFailureToItsStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"validation reads as the person's own mistake", &orderform.ValidationError{Message: "Builder is required."},
			http.StatusBadRequest, "Builder is required."},
		{"validation wrapped by a caller", fmt.Errorf("update: %w", &orderform.ValidationError{Message: "City is required."}),
			http.StatusBadRequest, "City is required."},
		{"the order has no form", orderform.ErrNotFound,
			http.StatusNotFound, "no customer order form"},
		{"the catalogue refused a name", inventory.ClientError{Msg: "An item name can be at most 150 characters."},
			http.StatusBadRequest, "at most 150"},
		{"the sales order refused a value", salesorder.ClientError{Msg: "Line 2: unit price cannot be negative."},
			http.StatusBadRequest, "unit price"},
		{"stock the order cannot have", &inventory.StockShortageError{},
			http.StatusConflict, ""},
		{"anything else is ours", errors.New("connection reset"),
			http.StatusInternalServerError, "Failed to update the customer order form."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/customer-order-form", nil)

			customerOrderFormFail(rr, req, tc.err)

			assert.Equal(t, tc.wantStatus, rr.Code)
			assert.Contains(t, rr.Body.String(), tc.wantBody)
		})
	}
}

func TestCustomerOrderFormFail_NeverLeaksAnInternalError(t *testing.T) {
	rr := httptest.NewRecorder()

	customerOrderFormFail(rr, httptest.NewRequest(http.MethodPut, "/x", nil),
		errors.New("pq: relation customer_order_form does not exist"))

	assert.NotContains(t, rr.Body.String(), "relation")
}
