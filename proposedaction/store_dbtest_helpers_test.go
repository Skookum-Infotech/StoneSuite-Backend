//go:build dbtest

package proposedaction

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/authz"
	"stonesuite-backend/provenance"
)

const testResource = authz.ResourceExpense

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// recExec is a configurable test executor that records what it saw.
type recExec struct {
	calls     atomic.Int32
	via       atomic.Value // provenance.Via
	markerKey string       // when set, Execute inserts a roles row with this key first
	execErr   error
	failValid atomic.Bool // when true, Validate rejects (simulates a payload gone stale)
	denyScope bool
}

func (e *recExec) Resource() authz.Resource { return testResource }

func (e *recExec) Validate(_ context.Context, payload json.RawMessage) error {
	if e.failValid.Load() {
		return errors.New("stale payload")
	}
	return NoopExecutor{}.Validate(context.Background(), payload)
}

func (e *recExec) Preview(ctx context.Context, payload json.RawMessage) (Preview, error) {
	return NoopExecutor{Res: testResource}.Preview(ctx, payload)
}

func (e *recExec) InScope(context.Context, Querier, authz.Scope, Caller, Kind, json.RawMessage) (bool, error) {
	return !e.denyScope, nil
}

func (e *recExec) Execute(ctx context.Context, tx pgx.Tx, _ Caller, _ json.RawMessage) (ResultRef, error) {
	e.calls.Add(1)
	e.via.Store(provenance.From(ctx))
	if e.markerKey != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO roles (key, name) VALUES ($1, $1)`, e.markerKey); err != nil {
			return ResultRef{}, err
		}
	}
	if e.execErr != nil {
		return ResultRef{}, e.execErr
	}
	return ResultRef{Resource: string(testResource), RecordID: "rec-" + time.Now().Format("150405.000000000"), Label: "L"}, nil
}

type fx struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *Store
	exec   *recExec
	caller Caller
	roleID string
}

// newFx seeds one user holding testResource create+update with the given scope.
func newFx(t *testing.T) *fx {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)
	f := &fx{t: t, ctx: ctx, pool: pool, exec: &recExec{}}
	reg := NewRegistry()
	require.NoError(t, reg.Register(KindCreate, f.exec))
	require.NoError(t, reg.Register(KindUpdate, f.exec))
	f.store = NewStore(pool, reg, time.Hour)

	var identity string
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&identity))
	var userID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email) VALUES ($1::uuid, $1::text || '@pa.test') RETURNING id`, identity).Scan(&userID))
	f.caller = Caller{IdentityID: identity, UserID: userID}
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO roles (key, name) VALUES ('pa_' || $1::text, 'pa_' || $1::text) RETURNING id`, identity).Scan(&f.roleID))
	for _, act := range []authz.Action{authz.ActionCreate, authz.ActionUpdate} {
		_, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, resource, action, scope) VALUES ($1, $2, $3, 'all')`,
			f.roleID, string(testResource), string(act))
		require.NoError(t, err)
	}
	require.NoError(t, authz.AssignRole(ctx, pool, userID, f.roleID))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ai_proposed_action WHERE identity_id = $1`, identity)
		_, _ = pool.Exec(bg, `DELETE FROM domain_event WHERE event_type = $1 AND actor_user_id = $2`, eventConfirmedType, userID)
		_, _ = pool.Exec(bg, `DELETE FROM roles WHERE id = $1 OR key LIKE 'pa_marker_%'`, f.roleID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	return f
}

func (f *fx) propose() string {
	f.t.Helper()
	id, err := f.store.Propose(f.ctx, f.pool, Proposal{
		Caller: f.caller, Kind: KindCreate, Resource: string(testResource),
		Source: SourceAgent, Payload: json.RawMessage(`{"name":"x"}`),
	})
	require.NoError(f.t, err)
	return id
}

func (f *fx) status(id string) Status {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT status FROM ai_proposed_action WHERE id = $1`, id).Scan(&s))
	return Status(s)
}
