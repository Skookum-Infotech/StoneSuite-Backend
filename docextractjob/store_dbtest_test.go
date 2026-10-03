//go:build dbtest

package docextractjob

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_CreateGetOwnerIsolation(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	e := newExtraction(t, s, ownerA)
	assert.Equal(t, StatusAwaitingUpload, e.Status)
	assert.Equal(t, StagingKey("acme", e.ID, "pdf"), e.StagingKey)
	assert.True(t, e.ExpiresAt.After(time.Now().Add(23*time.Hour)))

	got, err := s.Get(ctx, e.ID, ownerA)
	require.NoError(t, err)
	assert.Equal(t, e.ID, got.ID)

	_, err = s.Get(ctx, e.ID, ownerB)
	assert.ErrorIs(t, err, ErrNotFound, "another owner's id must look like a missing one")
	_, err = s.Get(ctx, "not-a-uuid", ownerA)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Discard(ctx, e.ID, ownerB)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, s.SetNotify(ctx, e.ID, ownerB, true), ErrNotFound)
	assert.ErrorIs(t, s.CompleteCAS(ctx, e.ID, ownerB, custA), ErrNotFound)
	_, _, err = s.MarkQueued(ctx, e.ID, ownerB, "", "job")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestStore_CreateRejectsBadExtension(t *testing.T) {
	s := NewStore(testPool(t))
	_, err := s.Create(context.Background(), CreateParams{DocType: "sales_order", OwnerIdentityID: ownerA, TenantSlug: "acme", Extension: "../x", TTL: time.Hour})
	assert.ErrorIs(t, err, ErrBadExtension)
}

func TestStore_LazyExpireDeletesRow(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	e := newExtraction(t, s, ownerA)
	_, err := pool.Exec(ctx, `UPDATE document_extractions SET expires_at = NOW() - interval '1 minute' WHERE id = $1::uuid`, e.ID)
	require.NoError(t, err)

	got, err := s.Get(ctx, e.ID, ownerA)
	assert.ErrorIs(t, err, ErrExpired)
	require.NotNil(t, got)
	assert.Equal(t, e.StagingKey, got.StagingKey, "caller needs the key to delete the object")
	_, err = s.Get(ctx, e.ID, ownerA)
	assert.ErrorIs(t, err, ErrNotFound, "expired row is gone")

	// An attached row never lazy-expires.
	a := newExtraction(t, s, ownerA)
	_, err = pool.Exec(ctx, `UPDATE document_extractions SET status = 'attached', expires_at = NOW() - interval '1 day' WHERE id = $1::uuid`, a.ID)
	require.NoError(t, err)
	_, err = s.Get(ctx, a.ID, ownerA)
	assert.NoError(t, err)
}

func TestStore_StateMachine(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	e := newExtraction(t, s, ownerA)

	st, changed, err := s.MarkQueued(ctx, e.ID, ownerA, "ABCDEF", "job-1")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, StatusQueued, st)
	st, changed, err = s.MarkQueued(ctx, e.ID, ownerA, "ABCDEF", "job-1")
	require.NoError(t, err)
	assert.False(t, changed, "second start is idempotent")
	assert.Equal(t, StatusQueued, st)

	row, err := s.GetInternal(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", row.ClientSHA256, "client sha is stored lower-case")

	require.NoError(t, s.MarkRunning(ctx, e.ID))
	require.NoError(t, s.MarkRunning(ctx, e.ID), "running is re-enterable after a requeue")
	require.NoError(t, s.SaveResult(ctx, e.ID, SaveResultParams{SHA256: "abc", Method: "parser", Result: ResultDoc{Duplicates: []Duplicate{}}}))
	assert.ErrorIs(t, s.SaveResult(ctx, e.ID, SaveResultParams{SHA256: "abc", Method: "parser"}), ErrConflict, "only a running row accepts a result")

	ready, err := s.ListReadyForOwner(ctx, ownerA)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	assert.Nil(t, ready[0].Result, "list omits the result body")
	other, err := s.ListReadyForOwner(ctx, ownerB)
	require.NoError(t, err)
	assert.Empty(t, other)

	won, err := s.ClaimReadyNotify(ctx, e.ID)
	require.NoError(t, err)
	assert.False(t, won, "no claim before the owner asked to be notified")
	require.NoError(t, s.SetNotify(ctx, e.ID, ownerA, true))
	won, err = s.ClaimReadyNotify(ctx, e.ID)
	require.NoError(t, err)
	assert.True(t, won, "a ready row whose owner asked to be notified is claimed")
	won, err = s.ClaimReadyNotify(ctx, e.ID)
	require.NoError(t, err)
	assert.False(t, won, "the ready notification is sent exactly once")

	rec := "33333333-3333-4333-8333-333333333333"
	require.NoError(t, s.CompleteCAS(ctx, e.ID, ownerA, rec))
	assert.ErrorIs(t, s.CompleteCAS(ctx, e.ID, ownerA, rec), ErrConflict, "second complete must fail")
	got, err := s.Get(ctx, e.ID, ownerA)
	require.NoError(t, err)
	assert.Equal(t, StatusUsed, got.Status)
	assert.Equal(t, rec, got.RecordUUID)

	_, err = s.Discard(ctx, e.ID, ownerA)
	assert.ErrorIs(t, err, ErrConflict, "a used extraction cannot be discarded")
	assert.ErrorIs(t, s.MarkRunning(ctx, e.ID), ErrConflict)
}

func TestStore_DiscardIsIdempotentAndBlocksRun(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	e := newExtraction(t, s, ownerA)
	_, _, err := s.MarkQueued(ctx, e.ID, ownerA, "", "job")
	require.NoError(t, err)

	key, err := s.Discard(ctx, e.ID, ownerA)
	require.NoError(t, err)
	assert.Equal(t, e.StagingKey, key)
	key, err = s.Discard(ctx, e.ID, ownerA)
	require.NoError(t, err)
	assert.Equal(t, e.StagingKey, key)
	assert.ErrorIs(t, s.MarkRunning(ctx, e.ID), ErrConflict, "a discarded extraction never runs")
}

func TestStore_MarkFailed(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	e := newExtraction(t, s, ownerA)
	_, _, err := s.MarkQueued(ctx, e.ID, ownerA, "", "job")
	require.NoError(t, err)
	require.NoError(t, s.MarkRunning(ctx, e.ID))
	require.NoError(t, s.MarkFailed(ctx, e.ID, "scanned"))
	got, err := s.Get(ctx, e.ID, ownerA)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, got.Status)
	assert.Equal(t, "scanned", got.FailureCode)
}

func TestStore_CountsCacheAndSweep(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	a := newExtraction(t, s, ownerA)
	b := newExtraction(t, s, ownerB)
	n, err := s.CountCreatedToday(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	sum, err := s.SumPendingStagingBytes(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), sum)

	// b finishes with a result; a later upload of the same file hits the cache.
	_, _, err = s.MarkQueued(ctx, b.ID, ownerB, "", "j")
	require.NoError(t, err)
	require.NoError(t, s.MarkRunning(ctx, b.ID))
	require.NoError(t, s.SaveResult(ctx, b.ID, SaveResultParams{SHA256: "deadbeef", Method: "parser+llm", Model: "m", Result: ResultDoc{}}))

	hit, err := s.FindCacheHit(ctx, "sales_order", "DEADBEEF", time.Hour, a.ID)
	require.NoError(t, err)
	require.NotNil(t, hit)
	assert.Equal(t, b.ID, hit.ID)
	assert.Equal(t, "parser+llm", hit.Method)
	miss, err := s.FindCacheHit(ctx, "sales_order", "deadbeef", time.Hour, b.ID)
	require.NoError(t, err)
	assert.Nil(t, miss, "an extraction never cache-hits itself")
	miss, err = s.FindCacheHit(ctx, "purchase_order", "deadbeef", time.Hour, a.ID)
	require.NoError(t, err)
	assert.Nil(t, miss, "cache is per doc type")

	dups, err := s.DuplicateBySHA(ctx, "sales_order", "deadbeef", a.ID)
	require.NoError(t, err)
	assert.Empty(t, dups, "a ready (unsaved) extraction is not a duplicate")
	require.NoError(t, s.CompleteCAS(ctx, b.ID, ownerB, "11111111-2222-4333-8444-555555555555"))
	dups, err = s.DuplicateBySHA(ctx, "sales_order", "deadbeef", a.ID)
	require.NoError(t, err)
	require.Len(t, dups, 1, "a used extraction is a duplicate")
	assert.Equal(t, DupSameFile, dups[0].Kind)
	assert.Empty(t, dups[0].RecordUUID)

	// Sweep removes only expired, non-attached rows and returns their keys.
	_, err = pool.Exec(ctx, `UPDATE document_extractions SET expires_at = NOW() - interval '1 hour' WHERE id = ANY($1::uuid[])`, []string{a.ID, b.ID})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE document_extractions SET status = 'attached' WHERE id = $1::uuid`, b.ID)
	require.NoError(t, err)
	keys, err := s.SweepExpired(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{a.StagingKey}, keys)
	_, err = s.GetInternal(ctx, b.ID)
	assert.NoError(t, err, "attached rows survive the sweep")
}
