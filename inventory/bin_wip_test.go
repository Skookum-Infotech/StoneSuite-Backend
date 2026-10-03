package inventory

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWIPBinMachineIsOptional(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wip     bool
		machine string
		valid   bool
	}{
		{"general WIP", true, "", true}, {"saw WIP", true, "Saw 1", true}, {"storage", false, "", true}, {"machine needs WIP", false, "Saw 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := BinInput{WarehouseUUID: "warehouse", Code: "A", Type: "floor", IsWIP: tc.wip, MachineLabel: tc.machine}
			err := validateBinInput(&in)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
