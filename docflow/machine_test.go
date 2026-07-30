package docflow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// sample mirrors the adjustment machine's shape: a linear approval chain with
// cancel reachable from every non-terminal state.
var sample = Machine{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"POST": true, "CANC": true},
	"POST": {},
	"CANC": {},
}

func TestMachineCan(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"forward along the chain", "DRFT", "PAPV", true},
		{"rejection sends it back", "PAPV", "DRFT", true},
		{"cancel from draft", "DRFT", "CANC", true},
		{"skipping approval", "DRFT", "POST", false},
		{"skipping the request", "DRFT", "APPV", false},
		{"posted is terminal", "POST", "CANC", false},
		{"cancelled is terminal", "CANC", "DRFT", false},
		{"unposting", "POST", "APPV", false},
		{"unknown source", "XXXX", "DRFT", false},
		{"unknown target", "DRFT", "XXXX", false},
		{"self", "DRFT", "DRFT", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sample.Can(tt.from, tt.to))
		})
	}
}

func TestMachineValidate(t *testing.T) {
	assert.NoError(t, sample.Validate("DRFT", "PAPV"))
	assert.ErrorIs(t, sample.Validate("DRFT", "POST"), ErrInvalidTransition)
	assert.True(t, errors.Is(sample.Validate("POST", "DRFT"), ErrInvalidTransition))
}

func TestMachineTerminalAndNext(t *testing.T) {
	assert.True(t, sample.IsTerminal("POST"))
	assert.True(t, sample.IsTerminal("CANC"))
	assert.False(t, sample.IsTerminal("DRFT"))
	// A status the machine has never heard of reads as terminal, which is the
	// safe default: it offers no moves rather than inventing one.
	assert.True(t, sample.IsTerminal("XXXX"))

	assert.ElementsMatch(t, []string{"POST", "CANC"}, sample.Next("APPV"))
	assert.ElementsMatch(t, []string{"PAPV", "CANC"}, sample.Next("DRFT"))
	assert.Empty(t, sample.Next("POST"))
}

func TestMachineKnown(t *testing.T) {
	assert.True(t, sample.Known("DRFT"))
	// Reachable only as a destination, never as a source: still known.
	assert.True(t, sample.Known("POST"))
	assert.False(t, sample.Known("RCVD"))
}

// Every state must be reachable from the opening one, or it is dead vocabulary
// the document can never enter — the kind of thing that survives a review
// because each edge looks reasonable on its own.
func TestMachineEveryStateIsReachable(t *testing.T) {
	seen := map[string]bool{"DRFT": true}
	queue := []string{"DRFT"}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for next := range sample[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	for code := range sample {
		assert.True(t, seen[code], "status %s is unreachable from DRFT", code)
	}
}
