package controllers

import (
	"net/http"

	"stonesuite-backend/dberr"
)

// failLoad writes the response for an error from loading one record by an id
// taken from the request path. A client-caused database error — above all a
// malformed id, which Postgres rejects as invalid input syntax for a uuid — is
// reported as the 4xx dberr.Map gives it, because a bad id is the caller's
// mistake and not an outage. Every other error stays a 500 carrying serverMsg.
//
// Handlers call it in place of a hard-coded 500 after their own not-found check,
// so a missing record is still a 404 and only genuine failures reach the 500.
func failLoad(w http.ResponseWriter, err error, serverMsg string) {
	if status, msg, ok := dberr.Map(err); ok {
		fail(w, status, msg)
		return
	}
	fail(w, http.StatusInternalServerError, serverMsg)
}
