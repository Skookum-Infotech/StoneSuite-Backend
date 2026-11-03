package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
	"stonesuite-backend/query"
)

// invoiceFail maps an invoice-module error to its HTTP status and message.
func invoiceFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, invoice.ErrNotFound):
		fail(w, http.StatusNotFound, "Invoice not found.")
	case errors.Is(err, invoice.ErrInvalidTransition),
		errors.Is(err, invoice.ErrVoidWithSettlement),
		errors.Is(err, invoice.ErrApprovalRequired),
		errors.Is(err, invoice.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, invoice.ErrVersionConflict):
		fail(w, http.StatusConflict, "This invoice was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, invoice.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce invoice.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		var pce payment.ClientError
		if errors.As(err, &pce) {
			fail(w, http.StatusBadRequest, pce.Error())
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		if status, msg, ok := dberr.Map(err); ok {
			fail(w, status, msg)
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}
