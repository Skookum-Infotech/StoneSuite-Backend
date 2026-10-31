package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorpayment"
)

// vendorPaymentFail maps a vendorpayment/vendorbill error to the correct
// HTTP status and writes the JSON error response.
func vendorPaymentFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, vendorpayment.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor payment not found.")
	case errors.Is(err, vendorpayment.ErrInvalidTransition),
		errors.Is(err, vendorpayment.ErrApprovalRequired),
		errors.Is(err, vendorpayment.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, vendorpayment.ErrVersionConflict):
		fail(w, http.StatusConflict, "This vendor payment was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, vendorpayment.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce vendorpayment.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Refund reach into the vendorbill package for the row lock and
		// rollup, so a bad bill surfaces as vendorbill.ClientError. Without this
		// arm it would fall through to 500.
		var ve vendorbill.ClientError
		if errors.As(err, &ve) {
			fail(w, http.StatusBadRequest, ve.Error())
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
