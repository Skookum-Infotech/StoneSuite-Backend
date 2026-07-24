package purchaseorder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanRollup(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		want     bool
	}{
		// Forward, as a posting drives it.
		{"issued order starts receiving", "SENT", "PART", true},
		{"issued order fully received at once", "SENT", "RCVD", true},
		{"partial completes", "PART", "RCVD", true},

		// Backward, as a void drives it. These are the moves the user-facing
		// map deliberately forbids — a person may not walk an order backwards,
		// but returned goods must be allowed to.
		{"fully received falls back to partial", "RCVD", "PART", true},
		{"fully received falls back to issued", "RCVD", "SENT", true},
		{"partial falls back to issued", "PART", "SENT", true},

		// Pre-issue statuses never receive.
		{"draft never rolls", "DRFT", "PART", false},
		{"pending approval never rolls", "PAPV", "PART", false},
		{"approved but unsent never rolls", "APPV", "PART", false},

		// Terminal statuses never reopen.
		{"closed stays closed", "CLSD", "PART", false},
		{"cancelled stays cancelled", "CANC", "RCVD", false},
		{"nothing reaches closed via rollup", "PART", "CLSD", false},
		{"nothing reaches cancelled via rollup", "SENT", "CANC", false},

		{"no self move", "PART", "PART", false},
		{"unknown source", "XXXX", "PART", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, canRollup(tt.from, tt.to))
		})
	}
}

// The rollup map may only ever name statuses the seeded PORD lifecycle
// actually has, and only receiving-phase ones.
func TestRollupTransitionsStayWithinReceivingStatuses(t *testing.T) {
	receiving := map[string]bool{"SENT": true, "PART": true, "RCVD": true}
	for from, tos := range rollupTransitions {
		assert.True(t, receiving[from], "rollup map has a non-receiving source status %q", from)
		for to := range tos {
			assert.True(t, receiving[to], "rollup map has a non-receiving target status %q", to)
		}
	}
}

// Forward rollup moves must also be legal user transitions; only the reverse
// moves are exclusive to the rollup path. This keeps the two maps from
// drifting into disagreement about what "receiving" means.
func TestForwardRollupsAgreeWithUserTransitions(t *testing.T) {
	forward := [][2]string{
		{"SENT", "PART"},
		{"SENT", "RCVD"},
		{"PART", "RCVD"},
	}
	for _, m := range forward {
		assert.True(t, canRollup(m[0], m[1]), "rollup should allow %s→%s", m[0], m[1])
		assert.True(t, CanTransition(m[0], m[1]), "user map should also allow %s→%s", m[0], m[1])
	}

	reverse := [][2]string{
		{"RCVD", "PART"},
		{"PART", "SENT"},
		{"RCVD", "SENT"},
	}
	for _, m := range reverse {
		assert.True(t, canRollup(m[0], m[1]), "rollup should allow %s→%s", m[0], m[1])
		assert.False(t, CanTransition(m[0], m[1]),
			"user map must NOT allow %s→%s — walking an order backwards is the rollup's privilege alone", m[0], m[1])
	}
}
