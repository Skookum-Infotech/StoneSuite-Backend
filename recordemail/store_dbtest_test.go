//go:build dbtest

package recordemail

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tenantPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return openPool(t, "TEST_DATABASE_URL")
}

func controlPlanePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return openPool(t, "TEST_CP_DATABASE_URL")
}

func openPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Skipf("%s not set; skipping dbtest", env)
	}
	pool, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// newID returns a fresh UUID string from the database, so these tests need no
// UUID library and every run uses ids no earlier run left rows under.
func newID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id))
	return id
}

func inbound(record, providerID, messageID, from, body string) Message {
	return Message{
		RecordID: record, WorkflowKey: "sales_order", Direction: DirectionIn,
		FromEmail: from, Subject: "Re: SO-1", Body: body, ProviderEmailID: providerID, MessageID: messageID,
	}
}

func outbound(record, to, body string) Message {
	return Message{
		RecordID: record, WorkflowKey: "sales_order", Direction: DirectionOut,
		FromName: "Harish", To: to, Subject: "Your Sales Order SO-1", Body: body,
	}
}

func TestInsert_IsIdempotentOnProviderIDAndMessageID(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := newID(t, pool)

	first, err := Insert(ctx, pool, inbound(rec, "em_"+rec, "<m1@x>", "p@gmail.com", "Hello"))
	require.NoError(t, err)
	assert.True(t, first)

	// A webhook retry carries the same provider id: no second copy.
	again, err := Insert(ctx, pool, inbound(rec, "em_"+rec, "<m1@x>", "p@gmail.com", "Hello"))
	require.NoError(t, err)
	assert.False(t, again)

	// The same RFC Message-ID under a new provider id (the mail reached us via a
	// second route) is the same message too.
	dup, err := Insert(ctx, pool, inbound(rec, "em_other_"+rec, "<m1@x>", "p@gmail.com", "Hello"))
	require.NoError(t, err)
	assert.False(t, dup)

	// Two outbound messages carry no ids and are all kept.
	for _, body := range []string{"one", "two"} {
		ok, err := Insert(ctx, pool, outbound(rec, "p@gmail.com", body))
		require.NoError(t, err)
		assert.True(t, ok)
	}

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	assert.Len(t, msgs, 3)
}

func TestList_OldestFirstAndScopedToRecord(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec, other := newID(t, pool), newID(t, pool)

	for _, body := range []string{"first", "second", "third"} {
		_, err := Insert(ctx, pool, outbound(rec, "p@gmail.com", body))
		require.NoError(t, err)
	}
	_, err := Insert(ctx, pool, outbound(other, "z@gmail.com", "not mine"))
	require.NoError(t, err)

	msgs, err := List(ctx, pool, rec)
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	assert.Equal(t, []string{"first", "second", "third"}, []string{msgs[0].Body, msgs[1].Body, msgs[2].Body})
	assert.Equal(t, DirectionOut, msgs[0].Direction)
	assert.Equal(t, "Harish", msgs[0].FromName)

	empty, err := List(ctx, pool, newID(t, pool))
	require.NoError(t, err)
	assert.NotNil(t, empty, "an empty conversation is an empty list, never nil")
	assert.Empty(t, empty)
}

func TestLatestAndReplyTarget(t *testing.T) {
	pool, ctx := tenantPool(t), context.Background()
	rec := newID(t, pool)

	latest, err := Latest(ctx, pool, rec)
	require.NoError(t, err)
	assert.Nil(t, latest, "no messages yet")
	target, err := ReplyTarget(ctx, pool, rec)
	require.NoError(t, err)
	assert.Equal(t, "", target)

	// Only the order email so far: reply to whoever it was sent to (first address).
	_, err = Insert(ctx, pool, outbound(rec, "priya@gmail.com, boss@acme.com", "Hello"))
	require.NoError(t, err)
	target, err = ReplyTarget(ctx, pool, rec)
	require.NoError(t, err)
	assert.Equal(t, "priya@gmail.com", target)

	// Once the customer writes back, replies go to whoever wrote last.
	_, err = Insert(ctx, pool, inbound(rec, "em_a_"+rec, "<a@x>", "priya@gmail.com", "Q1"))
	require.NoError(t, err)
	_, err = Insert(ctx, pool, inbound(rec, "em_b_"+rec, "<b@x>", "boss@acme.com", "Q2"))
	require.NoError(t, err)
	target, err = ReplyTarget(ctx, pool, rec)
	require.NoError(t, err)
	assert.Equal(t, "boss@acme.com", target)

	latest, err = Latest(ctx, pool, rec)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, "Q2", latest.Body)

	ids, err := InboundMessageIDs(ctx, pool, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{"<a@x>", "<b@x>"}, ids)
}

func insertTenant(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO tenants (slug, display_name) VALUES ($1, 'Reply Address Test') RETURNING id::text`,
		"reply-addr-"+newID(t, pool)[:8]).Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

func TestAddressStore_TokenIsStableUniqueAndResolves(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	store := NewAddressStore(pool)
	tenant := insertTenant(t, pool)
	recA, recB := newID(t, pool), newID(t, pool)

	tokenA, err := store.TokenFor(ctx, tenant, recA, "sales_order")
	require.NoError(t, err)
	assert.Regexp(t, tokenPattern, tokenA)

	// Every later mail about the record reuses the token, so replying to an old
	// email still files under the right record.
	again, err := store.TokenFor(ctx, tenant, recA, "sales_order")
	require.NoError(t, err)
	assert.Equal(t, tokenA, again)

	tokenB, err := store.TokenFor(ctx, tenant, recB, "sales_order")
	require.NoError(t, err)
	assert.NotEqual(t, tokenA, tokenB)

	got, err := store.Resolve(ctx, tokenA)
	require.NoError(t, err)
	assert.Equal(t, Target{TenantID: tenant, RecordID: recA, WorkflowKey: "sales_order"}, got)

	_, err = store.Resolve(ctx, "aaaaaaaaaaaaaaaaaaaa")
	assert.True(t, errors.Is(err, ErrAddressNotFound))
}

func TestAddressStore_ConcurrentFirstUseYieldsOneToken(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	store := NewAddressStore(pool)
	tenant, rec := insertTenant(t, pool), newID(t, pool)

	const workers = 8
	tokens := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			tok, err := store.TokenFor(ctx, tenant, rec, "sales_order")
			tokens <- tok
			errs <- err
		}()
	}
	seen := map[string]bool{}
	for i := 0; i < workers; i++ {
		require.NoError(t, <-errs)
		seen[<-tokens] = true
	}
	assert.Len(t, seen, 1, "racing first sends must agree on one reply address")
}

func TestAddressStore_RemovedWithItsTenant(t *testing.T) {
	pool, ctx := controlPlanePool(t), context.Background()
	store := NewAddressStore(pool)
	tenant, rec := insertTenant(t, pool), newID(t, pool)

	token, err := store.TokenFor(ctx, tenant, rec, "sales_order")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant)
	require.NoError(t, err)

	_, err = store.Resolve(ctx, token)
	assert.True(t, errors.Is(err, ErrAddressNotFound), "a purged tenant's reply addresses stop resolving")
}
