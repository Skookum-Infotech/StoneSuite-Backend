package controllers

import (
	"context"
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/query"
)

// loadIROrFail runs get and, when it fails, writes the response irFail gives for
// that error — a missing receipt is a 404 and a malformed id (Postgres 22P02) a
// 400, instead of every lookup failure being reported as a 500. It reports
// whether the receipt loaded; on false the response is already written.
func loadIROrFail(
	ctx context.Context, w http.ResponseWriter,
	get func(context.Context) (*itemreceipt.ItemReceipt, error),
) (*itemreceipt.ItemReceipt, bool) {
	ir, err := get(ctx)
	if err != nil {
		irFail(w, err, "Failed to load item receipt.")
		return nil, false
	}
	return ir, true
}

// irFail maps a store error to an HTTP response.
func irFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, itemreceipt.ErrNotFound):
		fail(w, http.StatusNotFound, "Item receipt not found.")
	case errors.Is(err, itemreceipt.ErrInvalidTransition),
		errors.Is(err, itemreceipt.ErrAlreadyPosted),
		errors.Is(err, itemreceipt.ErrPONotReceivable),
		errors.Is(err, itemreceipt.ErrMovementAlreadyApplied):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, itemreceipt.ErrVersionConflict):
		fail(w, http.StatusConflict, "This item receipt was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, itemreceipt.ErrOverReceipt):
		// 403, not 400: the request is well-formed and the quantities are real.
		// The caller simply is not permitted to accept an over-delivery.
		fail(w, http.StatusForbidden, err.Error())
	case itemreceipt.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
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
