package salesorder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsConvertible(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"DRFT", false}, {"PAPV", false}, {"CANC", false}, {"", false}, {"BOGUS", false},
		{"APPV", true}, {"OPEN", true}, {"PART", true}, {"FILL", true},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			assert.Equal(t, tc.want, IsConvertible(tc.code))
		})
	}
}
