package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/inventory"
	"stonesuite-backend/orderform"
	"stonesuite-backend/salesorder"
)

func TestCreatedOrderErrorResponse(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantFault  bool
	}{
		{"validation message is shown as is", &orderform.ValidationError{Message: "Builder is required."},
			http.StatusBadRequest, "Builder is required.", false},
		{"validation wrapped by a caller", fmt.Errorf("create: %w", &orderform.ValidationError{Message: "City is required."}),
			http.StatusBadRequest, "City is required.", false},
		{"account on hold", orderform.ErrCustomerNotActive,
			http.StatusConflict, customerNotActiveMessage, false},
		{"order is not this customer's, or gone", orderform.ErrNotFound,
			http.StatusNotFound, "Order not found.", false},
		{"staff moved the draft on", orderform.ErrNotEditable,
			http.StatusConflict, orderLockedMessage, false},
		{"a change that needs stock nobody has", fmt.Errorf("update: %w", &inventory.StockShortageError{}),
			http.StatusConflict, orderStockMessage, false},
		{"sales order refused a value", salesorder.ClientError{Msg: "Line 2: unit price cannot be negative."},
			http.StatusBadRequest, "Line 2: unit price cannot be negative.", false},
		{"catalogue refused a name", inventory.ClientError{Msg: "A kit name is required."},
			http.StatusBadRequest, "A kit name is required.", false},
		{"anything else is ours", errors.New("connection reset"),
			http.StatusInternalServerError, "We could not save your order. Please try again.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg, fault := createdOrderErrorResponse(tc.err)

			assert.Equal(t, tc.wantStatus, status)
			assert.Equal(t, tc.wantMsg, msg)
			assert.Equal(t, tc.wantFault, fault)
		})
	}
}

func TestCreatedOrderErrorResponse_NeverLeaksAnInternalError(t *testing.T) {
	_, msg, _ := createdOrderErrorResponse(errors.New("pq: relation customer_order_form does not exist"))

	assert.NotContains(t, msg, "relation")
}
