package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/bintransfer"
	"stonesuite-backend/query"
)

// TestBinTransferHandlers_RequireAuth: every endpoint sits behind the same
// authentication as the rest of the tenant API -- an unauthenticated call is a
// 401, never a 5xx or a panic, and none of them touches a database first.
func TestBinTransferHandlers_RequireAuth(t *testing.T) {
	h := NewBinTransferOps()
	handlers := map[string]http.HandlerFunc{
		"list":       h.List,
		"search":     h.Search,
		"create":     h.Create,
		"get":        h.Get,
		"update":     h.Update,
		"delete":     h.Delete,
		"transition": h.Transition,
		"approve":    h.Approve,
		"reject":     h.Reject,
		"complete":   h.Complete,
		"history":    h.History,
		// The per-bin history is the same trail's other face.
		"bin history": NewInventoryBinOps().History,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			// A well-formed body, so authentication -- not body validation -- is the
			// first thing to fail: Transition decodes its body before checking the
			// grant, because the target status picks which grant applies.
			req := httptest.NewRequest(http.MethodPost, "/bin-transfers",
				strings.NewReader(`{"toStatusCode":"CANC","reason":"x"}`))
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

func TestBinTransferFail_MapsErrorsToStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", bintransfer.ErrNotFound, http.StatusNotFound},
		{"wrapped not found", fmt.Errorf("load: %w", bintransfer.ErrNotFound), http.StatusNotFound},
		{"invalid transition", bintransfer.ErrInvalidTransition, http.StatusConflict},
		{"approval required", bintransfer.ErrApprovalRequired, http.StatusConflict},
		{"approval not required", bintransfer.ErrApprovalNotRequired, http.StatusConflict},
		{"not editable", bintransfer.ErrNotEditable, http.StatusConflict},
		{"not an approver", bintransfer.ErrNotApprover, http.StatusForbidden},
		{"client error", bintransfer.ClientError{Msg: "Line 1 needs a slab."}, http.StatusBadRequest},
		{"invalid filter", &query.InvalidFilterError{Field: "x", Msg: "unknown field"}, http.StatusBadRequest},
		{"reason required", approvalchain.ErrReasonRequired, http.StatusBadRequest},
		{"already rejected", approvalchain.ErrAlreadyRejected, http.StatusConflict},
		{"unexpected", errors.New("connection reset"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			binTransferFail(rr, tc.err, "server error")
			assert.Equal(t, tc.want, rr.Code)
		})
	}
}

// The instant bin move is retired: a move is requested through a bin transfer so
// that it can be approved and has a document. The refusal must name the way out,
// and be a 409 -- the endpoint is understood, the state of the world forbids it.
func TestRefuseDirectBinMove_PointsAtBinTransfers(t *testing.T) {
	rr := httptest.NewRecorder()
	refuseDirectBinMove(rr)
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "Bin Transfer")
	assert.Contains(t, rr.Body.String(), `"success":false`)
}

// A 500 must not leak the underlying error text to the client.
func TestBinTransferFail_ServerErrorHidesDetail(t *testing.T) {
	rr := httptest.NewRecorder()
	binTransferFail(rr, errors.New("pq: relation \"bin_transfer\" does not exist"), "Failed to load bin transfer.")
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "Failed to load bin transfer.")
	assert.NotContains(t, rr.Body.String(), "relation")
}
