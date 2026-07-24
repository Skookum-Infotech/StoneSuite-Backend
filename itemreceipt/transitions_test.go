package itemreceipt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		want     bool
	}{
		{"pending posts to partial", "PEND", "PART", true},
		{"pending posts to received", "PEND", "RCVD", true},
		{"pending voids", "PEND", "VOID", true},
		{"partial voids", "PART", "VOID", true},
		{"received voids", "RCVD", "VOID", true},

		{"partial cannot go back to pending", "PART", "PEND", false},
		{"received cannot go back to partial", "RCVD", "PART", false},
		{"received cannot go back to pending", "RCVD", "PEND", false},
		{"void is terminal", "VOID", "PEND", false},
		{"void cannot repost", "VOID", "RCVD", false},
		{"no self transition", "PEND", "PEND", false},

		{"unknown source", "XXXX", "PEND", false},
		{"unknown target", "PEND", "XXXX", false},
		{"empty codes", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanTransition(tt.from, tt.to))
		})
	}
}

func TestValidateTransition(t *testing.T) {
	require.NoError(t, ValidateTransition("PEND", "RCVD"))
	require.ErrorIs(t, ValidateTransition("VOID", "PEND"), ErrInvalidTransition)
	require.ErrorIs(t, ValidateTransition("RCVD", "PART"), ErrInvalidTransition)
}

func TestIsPosted(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"PEND", false},
		{"PART", true},
		{"RCVD", true},
		{"VOID", false}, // a void has given its quantities back
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPosted(tt.code))
		})
	}
}

// Every status the schema seeds for record_type 14 must appear in the map, or
// a status exists that the module can never reach.
func TestAllSeededStatusesAreReachable(t *testing.T) {
	seeded := []string{"PEND", "PART", "RCVD", "VOID"}
	for _, code := range seeded {
		t.Run(code, func(t *testing.T) {
			_, ok := allowedTransitions[code]
			assert.True(t, ok, "status %q has no entry in allowedTransitions", code)
		})
	}
	assert.Len(t, allowedTransitions, len(seeded), "allowedTransitions has a status the schema does not seed")
}
