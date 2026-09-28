package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execRecorder is a workflow.Querier that records Exec args; the observer only
// ever calls Exec.
type execRecorder struct{ calls [][]any }

func (e *execRecorder) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	e.calls = append(e.calls, args)
	return pgconn.CommandTag{}, nil
}
func (e *execRecorder) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (e *execRecorder) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }

func TestRagAuditObserver(t *testing.T) {
	tests := []struct {
		name, action, resource, id string
		wantOp                     string // "" = no enqueue
	}{
		{"module update re-indexes", "update", "quote", "id-1", "upsert"},
		{"module create re-indexes", "create", "quote", "id-2", "upsert"},
		{"module delete drops the vector", "delete", "quote", "id-3", "delete"},
		{"non-AI resource ignored", "update", "no_such_resource", "id-4", ""},
		{"empty resource id ignored", "update", "quote", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &execRecorder{}
			ragAuditObserver(context.Background(), rec, tt.action, tt.resource, tt.id)
			if tt.wantOp == "" {
				assert.Empty(t, rec.calls)
				return
			}
			require.Len(t, rec.calls, 1)
			// EnqueueVia args: sourceID, op, recordType
			assert.Equal(t, []any{tt.id, tt.wantOp, "quote"}, rec.calls[0])
		})
	}
}

func TestRagLoaderRouter_UnknownTypeFails(t *testing.T) {
	r := newRAGLoaderRouter(nil, nil)
	_, _, _, _, err := r.Load(context.Background(), "no_such_type", "id")
	assert.ErrorContains(t, err, "no AI loader")
}
