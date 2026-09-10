package controllers

import "testing"

// TestSalesOrderApproveFinalizeCondition documents and pins the exact
// condition this package's Approve handler uses to decide whether an
// approval call just finalized (customer-notify-worthy) versus recorded a
// partial sign-off (not worthy) -- "APPV" is the sales_order gate's
// TargetStatusCode (approvalchain/registry.go's "sales_order" entry).
func TestSalesOrderApproveFinalizeCondition(t *testing.T) {
	cases := []struct {
		name       string
		statusCode string
		wantNotify bool
	}{
		{"still gated after a partial sign-off", "PAPV", false},
		{"finalized this call", "APPV", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := approvalFinalized(tc.statusCode)
			if got != tc.wantNotify {
				t.Errorf("statusCode %q: got notify=%v, want %v", tc.statusCode, got, tc.wantNotify)
			}
		})
	}
}
