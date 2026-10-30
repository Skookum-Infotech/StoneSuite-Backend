package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/vendorbill"
)

func TestVBDuplicateBody(t *testing.T) {
	dup := &vendorbill.DuplicateDocumentError{ExistingUUID: "u-1", ExistingNumber: "VBIL-000007"}
	cases := []struct {
		name       string
		disclose   bool
		wantFields bool
	}{
		{"readable existing bill is named", true, true},
		{"unreadable existing bill is not disclosed", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := vbDuplicateBody(dup, tc.disclose)
			assert.Equal(t, false, body["success"])
			assert.Equal(t, "duplicate_document", body["code"])
			assert.NotEmpty(t, body["message"])
			if tc.wantFields {
				assert.Equal(t, "u-1", body["existingUuid"])
				assert.Equal(t, "VBIL-000007", body["existingNumber"])
				assert.Contains(t, body["message"], "VBIL-000007")
			} else {
				assert.NotContains(t, body, "existingUuid")
				assert.NotContains(t, body, "existingNumber")
				assert.NotContains(t, body["message"], "VBIL-000007")
			}
		})
	}
}
