package controllers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestShouldRecordHelpState guards the bug where cp_help_corpus_state was
// recorded even when some docs failed to embed or the prune failed, which
// would make the next boot believe the corpus was fully synced and skip
// retrying.
func TestShouldRecordHelpState(t *testing.T) {
	tests := []struct {
		name     string
		failed   int
		pruneErr error
		want     bool
	}{
		{"no failures, no prune error", 0, nil, true},
		{"some docs failed", 2, nil, false},
		{"prune failed", 0, errors.New("prune failed"), false},
		{"both failed", 3, errors.New("prune failed"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldRecordHelpState(tt.failed, tt.pruneErr))
		})
	}
}
