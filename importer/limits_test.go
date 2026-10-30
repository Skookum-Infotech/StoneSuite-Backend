package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStageTabularRejectsOversizedFile(t *testing.T) {
	w := &Worker{}
	tooMany := make([]map[string]string, maxImportRows+1)
	// rows (the store) is nil: the cap must refuse before any write.
	err := w.stageTabular(context.Background(), nil, "job", tooMany, nil, nil)
	require.ErrorIs(t, err, errImportRejected)
}

// flakyMarker fails MarkCommitted failures times, then succeeds, and records
// whether each attempt saw a live context.
type flakyMarker struct {
	rowUpdater
	failures int
	calls    int
	liveCtx  []bool
}

func (f *flakyMarker) MarkCommitted(ctx context.Context, _, _ string) error {
	f.calls++
	f.liveCtx = append(f.liveCtx, ctx.Err() == nil)
	if f.calls <= f.failures {
		return errors.New("transient")
	}
	return nil
}

func TestMarkCommitted(t *testing.T) {
	t.Run("retries a transient failure", func(t *testing.T) {
		m := &flakyMarker{failures: markCommittedAttempts - 1}
		require.NoError(t, markCommitted(context.Background(), m, "row", "rec"))
		assert.Equal(t, markCommittedAttempts, m.calls)
	})
	t.Run("gives up after the last attempt", func(t *testing.T) {
		m := &flakyMarker{failures: markCommittedAttempts}
		require.Error(t, markCommitted(context.Background(), m, "row", "rec"))
		assert.Equal(t, markCommittedAttempts, m.calls)
	})
	t.Run("survives the caller cancelling after the record was created", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m := &flakyMarker{}
		require.NoError(t, markCommitted(ctx, m, "row", "rec"))
		assert.Equal(t, []bool{true}, m.liveCtx)
	})
}
