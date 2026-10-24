//go:build dbtest

package proposedentity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/authz"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/tenancy"
)

const (
	envDSN          = "TEST_DATABASE_URL"
	envDSNSecondary = "TEST_DATABASE_URL_B"
	failedMessage   = "The action could not be completed."
	proposalTTL     = time.Hour
)

var createResources = []authz.Resource{authz.ResourceCustomer, authz.ResourceVendor, authz.ResourceInventoryItem}

func newPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s not set; skipping DB-backed test", env)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func v2Design(context.Context) (string, error) { return tenancy.DesignV2, nil }

func v1Design(context.Context) (string, error) { return tenancy.DesignV1, nil }

func newRegistry(t *testing.T, design func(context.Context) (string, error)) *proposedaction.Registry {
	t.Helper()
	reg := proposedaction.NewRegistry()
	for _, e := range []proposedaction.Executor{customerExecutor{designVersion: design}, vendorExecutor{}, itemExecutor{}} {
		require.NoError(t, reg.Register(proposedaction.KindCreate, e))
	}
	return reg
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *proposedaction.Store
	caller proposedaction.Caller
	unitID int
	tag    string
}

// newFixture seeds one user with create permission on the three resources.
func newFixture(t *testing.T, pool *pgxpool.Pool, design func(context.Context) (string, error)) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t, ctx: ctx, pool: pool, store: proposedaction.NewStore(pool, newRegistry(t, design), proposalTTL)}
	f.caller, f.tag = f.seedUser(true)
	require.NoError(t, pool.QueryRow(ctx, `SELECT unit_id FROM lkp_unit WHERE unit_code = 'EA'`).Scan(&f.unitID))
	return f
}

// seedUser creates an identity+user, optionally holding create on every resource.
func (f *fixture) seedUser(grant bool) (proposedaction.Caller, string) {
	t := f.t
	var identity, userID, roleID string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT gen_random_uuid()`).Scan(&identity))
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO users (identity_id, email) VALUES ($1::uuid, $1::text || '@pe.test') RETURNING id`, identity).Scan(&userID))
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO roles (key, name) VALUES ('pe_' || $1::text, 'pe_' || $1::text) RETURNING id`, identity).Scan(&roleID))
	if grant {
		for _, res := range createResources {
			_, err := f.pool.Exec(f.ctx, `INSERT INTO role_permissions (role_id, resource, action, scope) VALUES ($1, $2, 'create', 'all')`,
				roleID, string(res))
			require.NoError(t, err)
		}
	}
	require.NoError(t, authz.AssignRole(f.ctx, f.pool, userID, roleID))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.pool.Exec(bg, `DELETE FROM ai_proposed_action WHERE identity_id = $1`, identity)
		_, _ = f.pool.Exec(bg, `DELETE FROM roles WHERE id = $1`, roleID)
	})
	return proposedaction.Caller{IdentityID: identity, UserID: userID}, identity[:8]
}

func (f *fixture) name(kind string) string {
	// Base36 keeps the name free of long digit runs, which the sensitive-data
	// guard (Luhn) would randomly reject.
	return fmt.Sprintf("PE %s n%s", kind, strconv.FormatInt(time.Now().UnixNano(), 36))
}

func (f *fixture) ref() string {
	return fmt.Sprintf("%s:pe_%d", "0b0c1c52-3c5e-4c6e-9a53-1d2f6a7b8c9d", time.Now().UnixNano())
}

// payloadFor builds a valid payload and returns it with the record-count SQL for its name.
func (f *fixture) payloadFor(res authz.Resource, name string) (json.RawMessage, string) {
	m := map[string]any{"sourceRef": f.ref(), "name": name}
	var countSQL string
	switch res {
	case authz.ResourceCustomer:
		m["contactEmail"] = "pe@example.com"
		countSQL = `SELECT count(*) FROM customer WHERE customer_name = $1`
	case authz.ResourceVendor:
		m["address"] = "1 Quarry Rd"
		countSQL = `SELECT count(*) FROM vendor WHERE vendor_legal_name = $1`
	default:
		m["sku"] = "PE-" + fmt.Sprint(time.Now().UnixNano())
		m["unitId"] = f.unitID
		m["unitCode"] = "EA"
		m["unitPriceCents"] = 1250
		m["purchaseUnitPriceCents"] = 0
		countSQL = `SELECT count(*) FROM inventory_item WHERE inventory_item_name = $1`
	}
	return mustMarshal(f.t, m), countSQL
}

func mustMarshal(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func (f *fixture) propose(caller proposedaction.Caller, res authz.Resource, payload json.RawMessage) string {
	f.t.Helper()
	id, err := f.store.Propose(f.ctx, f.pool, proposedaction.Proposal{
		Caller: caller, Kind: proposedaction.KindCreate, Resource: string(res),
		Source: proposedaction.SourceDocExtract, Payload: payload})
	require.NoError(f.t, err)
	return id
}

func (f *fixture) count(sql, arg string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(f.ctx, sql, arg).Scan(&n))
	return n
}

func (f *fixture) status(id string) (status, msg string) {
	f.t.Helper()
	var m *string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT status, error_message FROM ai_proposed_action WHERE id = $1`, id).Scan(&status, &m))
	if m != nil {
		msg = *m
	}
	return status, msg
}

func TestFlow_HappyPath(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v2Design)
	for _, res := range createResources {
		t.Run(string(res), func(t *testing.T) {
			name := f.name(string(res))
			payload, countSQL := f.payloadFor(res, name)
			id := f.propose(f.caller, res, payload)

			a, replayed, err := f.store.Confirm(f.ctx, f.caller, id)
			require.NoError(t, err)
			assert.False(t, replayed)
			assert.Equal(t, proposedaction.StatusConfirmed, a.Status)
			require.NotNil(t, a.ResultRef)
			assert.Equal(t, string(res), a.ResultRef.Resource)
			assert.Equal(t, name, a.ResultRef.Label)
			assert.NotEmpty(t, a.ResultRef.RecordID)
			assert.Equal(t, 1, f.count(countSQL, name))

			var via string
			require.NoError(t, f.pool.QueryRow(f.ctx, `
				SELECT details->>'via' FROM audit_logs
				WHERE action = 'create' AND resource = $1 AND resource_id = $2`,
				string(res), a.ResultRef.RecordID).Scan(&via))
			assert.Equal(t, "docextract", via)
		})
	}
}

func TestFlow_NoPermission(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v2Design)
	for _, res := range createResources {
		t.Run(string(res), func(t *testing.T) {
			name := f.name(string(res))
			payload, countSQL := f.payloadFor(res, name)
			weak, _ := f.seedUser(false)
			id := f.propose(weak, res, payload)
			_, _, err := f.store.Confirm(f.ctx, weak, id)
			var fe *proposedaction.ForbiddenError
			require.ErrorAs(t, err, &fe)
			assert.Equal(t, 0, f.count(countSQL, name))
		})
	}
}

func TestFlow_CrossIdentityNotFound(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v2Design)
	other, _ := f.seedUser(true)
	for _, res := range createResources {
		t.Run(string(res), func(t *testing.T) {
			name := f.name(string(res))
			payload, countSQL := f.payloadFor(res, name)
			id := f.propose(f.caller, res, payload)
			_, _, err := f.store.Confirm(f.ctx, other, id)
			var nf *proposedaction.NotFoundError
			require.ErrorAs(t, err, &nf)
			assert.True(t, nf.Mismatch)
			assert.Equal(t, 0, f.count(countSQL, name))
		})
	}
}

func TestFlow_ReplayCreatesOnce(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v2Design)
	for _, res := range createResources {
		t.Run(string(res), func(t *testing.T) {
			name := f.name(string(res))
			payload, countSQL := f.payloadFor(res, name)
			id := f.propose(f.caller, res, payload)
			first, _, err := f.store.Confirm(f.ctx, f.caller, id)
			require.NoError(t, err)
			second, replayed, err := f.store.Confirm(f.ctx, f.caller, id)
			require.NoError(t, err)
			assert.True(t, replayed)
			require.NotNil(t, second.ResultRef)
			assert.Equal(t, first.ResultRef.RecordID, second.ResultRef.RecordID)
			assert.Equal(t, 1, f.count(countSQL, name))
		})
	}
}

func TestFlow_Expired(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v2Design)
	for _, res := range createResources {
		t.Run(string(res), func(t *testing.T) {
			name := f.name(string(res))
			payload, countSQL := f.payloadFor(res, name)
			id := f.propose(f.caller, res, payload)
			_, err := f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
			require.NoError(t, err)
			_, _, err = f.store.Confirm(f.ctx, f.caller, id)
			assert.ErrorIs(t, err, proposedaction.ErrExpired)
			assert.Equal(t, 0, f.count(countSQL, name))
		})
	}
}

func TestFlow_DuplicateNameFailsAtomically(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v2Design)
	for _, res := range createResources {
		t.Run(string(res), func(t *testing.T) {
			name := f.name(string(res))
			payload1, countSQL := f.payloadFor(res, name)
			_, _, err := f.store.Confirm(f.ctx, f.caller, f.propose(f.caller, res, payload1))
			require.NoError(t, err)

			payload2, _ := f.payloadFor(res, name) // same name, fresh SKU / sourceRef
			id := f.propose(f.caller, res, payload2)
			_, _, err = f.store.Confirm(f.ctx, f.caller, id)
			assert.ErrorIs(t, err, proposedaction.ErrExecutionFailed)

			status, msg := f.status(id)
			assert.Equal(t, string(proposedaction.StatusFailed), status)
			assert.Equal(t, failedMessage, msg)
			assert.Equal(t, 1, f.count(countSQL, name), "failed create must leave no second record")
		})
	}
}

func TestFlow_CustomerRejectedOnV1Design(t *testing.T) {
	f := newFixture(t, newPool(t, envDSN), v1Design)
	name := f.name("customer")
	payload, countSQL := f.payloadFor(authz.ResourceCustomer, name)
	id := f.propose(f.caller, authz.ResourceCustomer, payload)
	_, _, err := f.store.Confirm(f.ctx, f.caller, id)
	assert.ErrorIs(t, err, proposedaction.ErrExecutionFailed)
	assert.Equal(t, 0, f.count(countSQL, name))
}

func TestFlow_TwoTenantIsolation(t *testing.T) {
	a := newFixture(t, newPool(t, envDSN), v2Design)
	b := newFixture(t, newPool(t, envDSNSecondary), v2Design)
	name := a.name("vendor")
	payload, countSQL := a.payloadFor(authz.ResourceVendor, name)
	id := a.propose(a.caller, authz.ResourceVendor, payload)

	// Tenant B's store cannot see or run tenant A's proposal, even for the same caller ids.
	_, _, err := b.store.Confirm(b.ctx, a.caller, id)
	var nf *proposedaction.NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, 0, b.count(countSQL, name))
	status, _ := a.status(id)
	assert.Equal(t, string(proposedaction.StatusPending), status, "A's proposal is untouched")

	_, _, err = a.store.Confirm(a.ctx, a.caller, id)
	require.NoError(t, err)
	assert.Equal(t, 1, a.count(countSQL, name))
	assert.Equal(t, 0, b.count(countSQL, name))
}
