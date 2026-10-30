package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/fabrication"
	"stonesuite-backend/query"
)

// fjFail maps a store error to an HTTP response.
func fjFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, fabrication.ErrNotFound):
		fail(w, http.StatusNotFound, "Fabrication job not found.")
	case errors.Is(err, fabrication.ErrInvalidTransition),
		errors.Is(err, fabrication.ErrApprovalRequired),
		errors.Is(err, fabrication.ErrApprovalNotRequired),
		errors.Is(err, fabrication.ErrDispositionRequired),
		errors.Is(err, fabrication.ErrPiecesLocked),
		errors.Is(err, fabrication.ErrPieceHasSlabs),
		errors.Is(err, fabrication.ErrJobHasDependents):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, fabrication.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case fabrication.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}
