package vendorbill

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDuplicateExtra(t *testing.T) {
	cases := []struct {
		name string
		dup  string
		want map[string]any
	}{
		{"no override", "", nil},
		{"override names the existing bill", "u-9", map[string]any{"duplicate_override": true, "existing_uuid": "u-9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, duplicateExtra(tc.dup))
		})
	}
}
