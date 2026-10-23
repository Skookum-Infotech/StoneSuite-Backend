package proposedaction

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/authz"
)

func TestRegistry_Register(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(r *Registry)
		kind    Kind
		ex      Executor
		wantErr bool
	}{
		{"ok", func(*Registry) {}, KindCreate, NoopExecutor{Res: authz.ResourceExpense}, false},
		{"nil executor", func(*Registry) {}, KindCreate, nil, true},
		{"bad kind", func(*Registry) {}, Kind("delete"), NoopExecutor{Res: authz.ResourceExpense}, true},
		{"empty resource", func(*Registry) {}, KindCreate, NoopExecutor{}, true},
		{"duplicate", func(r *Registry) { _ = r.Register(KindCreate, NoopExecutor{Res: authz.ResourceExpense}) },
			KindCreate, NoopExecutor{Res: authz.ResourceExpense}, true},
		{"same resource other kind", func(r *Registry) { _ = r.Register(KindCreate, NoopExecutor{Res: authz.ResourceExpense}) },
			KindUpdate, NoopExecutor{Res: authz.ResourceExpense}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			tt.setup(r)
			err := r.Register(tt.kind, tt.ex)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			_, ok := r.Lookup(string(authz.ResourceExpense), tt.kind)
			assert.True(t, ok)
		})
	}
}

func TestRegistry_Lookup(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(KindCreate, NoopExecutor{Res: authz.ResourceExpense}))
	_, ok := r.Lookup(string(authz.ResourceExpense), KindUpdate)
	assert.False(t, ok)
	_, ok = r.Lookup("other", KindCreate)
	assert.False(t, ok)
	var nilReg *Registry
	_, ok = nilReg.Lookup("x", KindCreate)
	assert.False(t, ok)
}

func TestCursorRoundTrip(t *testing.T) {
	c := listCursor{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123000, time.UTC), ID: "11111111-1111-1111-1111-111111111111"}
	s, err := encodeCursor(c)
	require.NoError(t, err)
	got, err := decodeCursor(s)
	require.NoError(t, err)
	assert.True(t, c.CreatedAt.Equal(got.CreatedAt))
	assert.Equal(t, c.ID, got.ID)
}

func TestDecodeCursor_Invalid(t *testing.T) {
	for name, in := range map[string]string{
		"not base64": "!!!",
		"not json":   "aGVsbG8",
		"empty obj":  "e30",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCursor(in)
			assert.ErrorIs(t, err, ErrInvalidCursor)
		})
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct{ in, want int }{{0, 25}, {-3, 25}, {10, 10}, {100, 100}, {500, 100}}
	for _, tt := range tests {
		assert.Equal(t, tt.want, clampLimit(tt.in))
	}
}

func TestErrorsIs(t *testing.T) {
	assert.ErrorIs(t, &NotFoundError{Mismatch: true}, ErrNotFound)
	assert.ErrorIs(t, &ForbiddenError{}, ErrForbidden)
	assert.ErrorIs(t, &ValidationError{Message: "x"}, ErrInvalidPayload)
	assert.False(t, errors.Is(&NotFoundError{}, ErrForbidden))
	var nf *NotFoundError
	require.ErrorAs(t, error(&NotFoundError{Mismatch: true}), &nf)
	assert.True(t, nf.Mismatch)
}

func TestAsValidation(t *testing.T) {
	orig := &ValidationError{Message: "m"}
	assert.Same(t, orig, asValidation(orig))
	var ve *ValidationError
	require.ErrorAs(t, asValidation(errors.New("boom")), &ve)
	assert.Equal(t, "boom", ve.Message)
}

func TestNoopExecutor(t *testing.T) {
	n := NoopExecutor{Res: authz.ResourceExpense}
	ctx := context.Background()
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"object with name", `{"name":"a"}`, false},
		{"empty object", `{}`, false},
		{"array", `[1]`, true},
		{"string", `"x"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := n.Validate(ctx, json.RawMessage(tt.payload))
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
	ref, err := n.Execute(ctx, nil, Caller{}, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, ResultRef{Resource: "expense", RecordID: "noop"}, ref)
}

func TestActionJSONOmitsPayload(t *testing.T) {
	b, err := json.Marshal(Action{ID: "x", Payload: json.RawMessage(`{"secret":1}`), IdentityID: "i"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "secret")
	assert.NotContains(t, string(b), "payload")
	assert.NotContains(t, string(b), "identity")
}

func TestViaFor(t *testing.T) {
	assert.EqualValues(t, "ai_agent", viaFor(SourceAgent))
	assert.EqualValues(t, "docextract", viaFor(SourceDocExtract))
}
