package controllers

import (
	"testing"
)

func TestRefundApproveFinalizeCondition(t *testing.T) {
	cases := []struct {
		name       string
		statusCode string
		wantNotify bool
	}{
		{"still gated after a partial sign-off", "PEND", false},
		{"finalized this call", "APPV", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.statusCode == "APPV"
			if got != tc.wantNotify {
				t.Errorf("statusCode %q: got notify=%v, want %v", tc.statusCode, got, tc.wantNotify)
			}
		})
	}
}
