package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/creditmemo"
	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/invoice"
	"stonesuite-backend/query"
)

// creditMemoFail maps a credit memo store error to an HTTP response.
func creditMemoFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, creditmemo.ErrNotFound):
		fail(w, http.StatusNotFound, "Credit memo not found.")
	case errors.Is(err, creditmemo.ErrInvalidTransition),
		errors.Is(err, creditmemo.ErrApprovalRequired),
		errors.Is(err, creditmemo.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, creditmemo.ErrVersionConflict):
		fail(w, http.StatusConflict, "This credit memo was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, creditmemo.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce creditmemo.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Unapply reach into the invoice package for the row lock and the
		// AR rollup, so a bad invoice surfaces as invoice.ClientError.
		var ice invoice.ClientError
		if errors.As(err, &ice) {
			fail(w, http.StatusBadRequest, ice.Error())
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
