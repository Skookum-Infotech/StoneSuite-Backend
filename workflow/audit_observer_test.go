package workflow

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditStubQuerier accepts the audit INSERT so LogAuditFull reaches dispatch.
type auditStubQuerier struct{}

func (auditStubQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (auditStubQuerier) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (auditStubQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func logOne(t *testing.T) {
	t.Helper()
	err := LogAuditFull(context.Background(), auditStubQuerier{},
		"user-1", "update", "invoice", "42", "invoice", nil, nil, nil, "", "", "")
	require.NoError(t, err)
}

func TestLogAuditFull_NotifiesEveryObserverWithActor(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	var got []AuditEvent
	RegisterAuditObserver(func(_ context.Context, _ Querier, ev AuditEvent) { got = append(got, ev) })
	RegisterAuditObserver(func(_ context.Context, _ Querier, ev AuditEvent) { got = append(got, ev) })

	logOne(t)

	want := AuditEvent{Action: "update", Resource: "invoice", ResourceID: "42", ActorUserID: "user-1"}
	assert.Equal(t, []AuditEvent{want, want}, got)
}

func TestLogAuditFull_NoObserversIsFine(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	logOne(t)
}

func TestLogAuditFull_ObserverPanicIsolated(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	called := false
	RegisterAuditObserver(func(context.Context, Querier, AuditEvent) { panic("boom") })
	RegisterAuditObserver(func(context.Context, Querier, AuditEvent) { called = true })

	assert.NotPanics(t, func() { logOne(t) })
	assert.True(t, called, "second observer must still run after the first panics")
}

func TestRegisterAuditObserver_IgnoresNil(t *testing.T) {
	t.Cleanup(resetAuditObservers)
	resetAuditObservers()
	RegisterAuditObserver(nil)
	logOne(t)
}
