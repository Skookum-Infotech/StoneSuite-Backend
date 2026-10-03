package approvalchain

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSequenceDecision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		steps   []SequenceStep
		actor   int
		wantErr bool
		done    bool
	}{
		{"unconfigured fails closed", nil, 1, true, false},
		{"first stage", []SequenceStep{{EmployeeID: 1}, {EmployeeID: 2}}, 1, false, false},
		{"cannot skip first", []SequenceStep{{EmployeeID: 1}, {EmployeeID: 2}}, 2, true, false},
		{"last stage", []SequenceStep{{EmployeeID: 1, Approved: true}, {EmployeeID: 2}}, 2, false, true},
		{"not approver", []SequenceStep{{EmployeeID: 1}}, 3, true, false},
		{"already done", []SequenceStep{{EmployeeID: 1, Approved: true}}, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, done, err := NextSequenceDecision(tc.steps, tc.actor)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.done, done)
			}
		})
	}
}
