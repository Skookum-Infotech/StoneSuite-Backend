package docextractjob

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// bookkeepingContext must survive the worker's shutdown cancel (else a job's
// outcome is lost and it sits 'running' until the reaper) yet stay bounded.
func TestBookkeepingContext(t *testing.T) {
	tests := []struct {
		name         string
		cancelParent bool
	}{
		{name: "live parent", cancelParent: false},
		{name: "parent cancelled by Stop", cancelParent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelParent {
				cancel()
			}
			ctx, done := bookkeepingContext(parent)
			defer done()

			assert.NoError(t, ctx.Err(), "bookkeeping must not inherit the parent's cancellation")
			deadline, ok := ctx.Deadline()
			assert.True(t, ok, "bookkeeping must be bounded")
			assert.WithinDuration(t, time.Now().Add(bookkeepingTimeout), deadline, time.Second)
		})
	}
}
