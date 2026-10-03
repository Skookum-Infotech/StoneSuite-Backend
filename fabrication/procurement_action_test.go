package fabrication

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestShortagePurchaseAction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   [5]bool
		blocker string
	}{
		{"ready", [5]bool{true, true, true, true, true}, ""},
		{"inactive", [5]bool{false, true, true, true, true}, "job_state"},
		{"ownership", [5]bool{true, false, true, true, true}, "job_permission"},
		{"purchase permission", [5]bool{true, true, false, true, true}, "purchase_permission"},
		{"employee", [5]bool{true, true, true, false, true}, "employee_required"},
		{"stale approval", [5]bool{true, true, true, true, false}, "template_approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := shortagePurchaseAction(tc.flags[0], tc.flags[1], tc.flags[2], tc.flags[3], tc.flags[4])
			assert.Equal(t, tc.blocker == "", a.Enabled)
			if tc.blocker != "" {
				if assert.Len(t, a.Blockers, 1) {
					assert.Equal(t, tc.blocker, a.Blockers[0].Code)
				}
			} else {
				assert.Empty(t, a.Blockers)
			}
		})
	}
}
