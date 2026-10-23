package domainevent

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/provenance"
)

type fakeQ struct {
	args []any
	n    int
}

func (f *fakeQ) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.n++
	f.args = args
	return pgconn.CommandTag{}, nil
}

func (f *fakeQ) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func TestEmitValidation(t *testing.T) {
	tests := []struct {
		name    string
		ev      Event
		wantErr error
		inserts int
	}{
		{"ok", Event{Type: "t", Resource: "r", RecordID: "1"}, nil, 1},
		{"max depth ok", Event{Type: "t", Resource: "r", RecordID: "1", Depth: MaxDepth}, nil, 1},
		{"depth too high", Event{Type: "t", Resource: "r", RecordID: "1", Depth: MaxDepth + 1}, ErrDepthExceeded, 0},
		{"depth negative", Event{Type: "t", Resource: "r", RecordID: "1", Depth: -1}, ErrDepthExceeded, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeQ{}
			err := Emit(context.Background(), q, tt.ev)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.inserts, q.n)
		})
	}
}

func TestEmitRequiredFields(t *testing.T) {
	for _, ev := range []Event{
		{Resource: "r", RecordID: "1"},
		{Type: "t", RecordID: "1"},
		{Type: "t", Resource: "r"},
	} {
		q := &fakeQ{}
		assert.Error(t, Emit(context.Background(), q, ev))
		assert.Zero(t, q.n)
	}
}

func TestEmitDefaultsAndArgs(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		ev      Event
		wantVia string
		actor   bool
		payload string
	}{
		{"ctx default user", context.Background(), Event{Type: "t", Resource: "r", RecordID: "1"}, "user", false, "{}"},
		{"ctx automation", provenance.With(context.Background(), provenance.Automation), Event{Type: "t", Resource: "r", RecordID: "1"}, "automation", false, "{}"},
		{"explicit via wins", provenance.With(context.Background(), provenance.Automation),
			Event{Type: "t", Resource: "r", RecordID: "1", Via: provenance.AIAgent, ActorUserID: "u1", Payload: map[string]int{"a": 1}}, "ai_agent", true, `{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeQ{}
			require.NoError(t, Emit(tt.ctx, q, tt.ev))
			require.Len(t, q.args, 7)
			assert.Equal(t, tt.wantVia, q.args[4])
			assert.Equal(t, tt.payload, q.args[6])
			actor, _ := q.args[3].(*string)
			assert.Equal(t, tt.actor, actor != nil)
		})
	}
}

func TestChildDepth(t *testing.T) {
	assert.Equal(t, int16(1), ChildDepth(Stored{Event: Event{Depth: 0}}))
	assert.Equal(t, int16(3), ChildDepth(Stored{Event: Event{Depth: 2}}))
}

func TestRegistry(t *testing.T) {
	h := func(context.Context, pgx.Tx, Stored) error { return nil }
	r := NewRegistry()
	require.NoError(t, r.Register("b", h))
	require.NoError(t, r.Register("a", h))
	assert.Error(t, r.Register("a", h), "duplicate")
	assert.Error(t, r.Register("", h), "empty name")
	assert.Error(t, r.Register("c", nil), "nil handler")
	assert.Equal(t, []string{"a", "b"}, r.Names())
	_, ok := r.Handler("a")
	assert.True(t, ok)
	_, ok = r.Handler("zzz")
	assert.False(t, ok)
}

func TestPruneRejectsBadRetention(t *testing.T) {
	_, err := Prune(context.Background(), nil, 0)
	assert.Error(t, err)
}

func TestDrainUnknownConsumer(t *testing.T) {
	_, err := Drain(context.Background(), nil, NewRegistry(), "nope", 1)
	assert.Error(t, err)
}
