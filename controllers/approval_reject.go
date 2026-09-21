package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/approvalchain"
)

// approvalRejectStatus maps the errors approvalchain adds for a rejection
// (a missing or over-long reason, a record that was already rejected, a gate
// that takes no reject) onto HTTP statuses, so every module's *Fail helper
// answers them the same way instead of each restating the switch. ok is false
// for any other error, which the caller maps as it always did.
func approvalRejectStatus(err error) (status int, ok bool) {
	switch {
	case errors.Is(err, approvalchain.ErrReasonRequired), errors.Is(err, approvalchain.ErrReasonTooLong):
		return http.StatusBadRequest, true
	case errors.Is(err, approvalchain.ErrAlreadyRejected), errors.Is(err, approvalchain.ErrRejectNotSupported):
		return http.StatusConflict, true
	}
	return 0, false
}
