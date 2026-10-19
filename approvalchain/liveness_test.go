package approvalchain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequiredApprovals(t *testing.T) {
	tests := []struct {
		name             string
		configured, live int
		want             int
		wantOrphaned     bool
	}{
		{"no approvers configured is ungated", 0, 0, 0, false},
		{"all live", 3, 3, 3, false},
		{"some deactivated lowers quorum", 3, 1, 1, false},
		{"all deactivated fails closed", 2, 0, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RequiredApprovals(tc.configured, tc.live))
			assert.Equal(t, tc.wantOrphaned, IsOrphaned(tc.configured, tc.live))
		})
	}
}
