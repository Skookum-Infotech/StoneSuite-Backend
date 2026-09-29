package controllers

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docpdf"
)

func TestPrepareSend(t *testing.T) {
	meta := DocMeta{WorkflowKey: "purchase_order", Number: "PO-1", DefaultRecipientEmail: "v@acme.com", DefaultSubject: "Purchase Order PO-1"}
	cases := []struct {
		name       string
		meta       DocMeta
		req        sendDocRequest
		wantTo     []string
		wantSubj   string
		wantStatus int
	}{
		{"defaults to vendor email and subject", meta, sendDocRequest{}, []string{"v@acme.com"}, "Purchase Order PO-1", 0},
		{"override wins", meta, sendDocRequest{To: []string{" a@b.co "}, Subject: "Hi"}, []string{"a@b.co"}, "Hi", 0},
		{"no recipient anywhere", DocMeta{}, sendDocRequest{}, nil, "", http.StatusBadRequest},
		{"bad address", meta, sendDocRequest{To: []string{"nope"}}, nil, "", http.StatusBadRequest},
		{"bad cc", meta, sendDocRequest{CC: []string{"x\r\n@y.z"}}, nil, "", http.StatusBadRequest},
		{"header injection in subject", meta, sendDocRequest{Subject: "a\r\nBcc: x@y.z"}, nil, "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, serr := prepareSend("t1", "rec1", docpdf.PrintableDoc{}, tc.meta, tc.req)
			if tc.wantStatus != 0 {
				require.NotNil(t, serr)
				assert.Equal(t, tc.wantStatus, serr.Status)
				return
			}
			require.Nil(t, serr)
			assert.Equal(t, tc.wantTo, p.to)
			assert.Equal(t, tc.wantSubj, p.subject)
		})
	}
}
