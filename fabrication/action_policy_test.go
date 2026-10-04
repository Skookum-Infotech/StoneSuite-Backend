package fabrication

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPieceActionsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     PieceActionState
		permitted bool
		code      string
		enabled   bool
	}{
		{"no grant", PieceActionState{Stage: StageEdging}, false, "", false},
		{"unknown state", PieceActionState{Stage: "bad"}, true, "", false},
		{"held", PieceActionState{Stage: StageEdging, Held: true}, true, "complete_edging", false},
		{"edging", PieceActionState{Stage: StageEdging}, true, "complete_edging", true},
		{"qc", PieceActionState{Stage: StageQC}, true, "record_qc", true},
		{"pending remake", PieceActionState{Stage: StageQCPassed, RemakePending: true}, true, "create_handover", false},
		{"ready handover", PieceActionState{Stage: StageQCPassed}, true, "create_handover", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actions := PieceActions(tc.state, tc.permitted)
			if tc.code == "" {
				require.Empty(t, actions)
				return
			}
			require.NotEmpty(t, actions)
			require.Equal(t, tc.code, actions[0].Code)
			require.Equal(t, tc.enabled, actions[0].Enabled)
			if !tc.enabled {
				require.NotEmpty(t, actions[0].Blockers)
			}
		})
	}
}
