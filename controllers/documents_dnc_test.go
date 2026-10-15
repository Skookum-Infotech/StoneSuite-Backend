package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckDoNotContact(t *testing.T) {
	const custID = "cust-1"
	cases := []struct {
		name       string
		meta       DocMeta
		dnc        bool
		lookupErr  error
		wantCalled bool
		wantStatus int // 0 = allowed
		wantDNC    bool
	}{
		{"vendor doc without customer skips lookup", DocMeta{WorkflowKey: "vendor_bill"}, true, nil, false, 0, false},
		{"customer may be contacted", DocMeta{WorkflowKey: "invoice", CustomerID: custID}, false, nil, true, 0, false},
		{"customer flagged do not contact", DocMeta{WorkflowKey: "invoice", CustomerID: custID}, true, nil, true, http.StatusConflict, true},
		{"lookup failure fails closed", DocMeta{WorkflowKey: "quote", CustomerID: custID}, false, errors.New("db down"), true, http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := NewDocumentOps(nil, nil, nil)
			h.doNotContact = func(_ context.Context, _ *pgxpool.Pool, id string) (bool, error) {
				called = true
				assert.Equal(t, custID, id)
				return tc.dnc, tc.lookupErr
			}
			serr := h.checkDoNotContact(context.Background(), nil, tc.meta, "rec-1", "ident-1")
			assert.Equal(t, tc.wantCalled, called)
			if tc.wantStatus == 0 {
				assert.Nil(t, serr)
				return
			}
			require.NotNil(t, serr)
			assert.Equal(t, tc.wantStatus, serr.Status)
			assert.Equal(t, tc.wantDNC, serr.DoNotContact)
		})
	}
}

func TestWriteSendError(t *testing.T) {
	cases := []struct {
		name          string
		serr          *sendError
		wantStatus    int
		wantEmailSent bool // the key must be present and false
	}{
		{"do not contact carries emailSent false", &sendError{Status: http.StatusConflict, Msg: msgDoNotContact, DoNotContact: true}, http.StatusConflict, true},
		{"other failures keep the plain shape", &sendError{Status: http.StatusBadRequest, Msg: "Invalid subject."}, http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			writeSendError(rr, tc.serr)
			assert.Equal(t, tc.wantStatus, rr.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal(t, false, body["success"])
			assert.Equal(t, tc.serr.Msg, body["message"])
			sent, ok := body["emailSent"]
			assert.Equal(t, tc.wantEmailSent, ok)
			if tc.wantEmailSent {
				assert.Equal(t, false, sent)
				assert.Equal(t, true, body["doNotContact"])
			}
		})
	}
}
