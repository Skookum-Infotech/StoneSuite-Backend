package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
)

// vendorCreditFail maps a store error to an HTTP response.
func vendorCreditFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, vendorcredit.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor credit not found.")
	case errors.Is(err, vendorcredit.ErrInvalidTransition),
		errors.Is(err, vendorcredit.ErrApprovalRequired),
		errors.Is(err, vendorcredit.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, vendorcredit.ErrVersionConflict):
		fail(w, http.StatusConflict, "This vendor credit was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, vendorcredit.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce vendorcredit.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Reverse reach into the vendorbill package for the row lock and
		// the AP rollup, so a bad vendor bill surfaces as vendorbill.ClientError.
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
