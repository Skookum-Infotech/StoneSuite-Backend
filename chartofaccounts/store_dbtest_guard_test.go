//go:build dbtest

package chartofaccounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardRetireBlocksAllFourMutations is AD-7's whole purpose: deactivate,
// hide, un-post, and delete are all refused for an account a default slot
// still points at, and each refusal is a 409 ConflictError, not a 500 from a
// constraint the guard was supposed to prevent from ever firing.
func TestGuardRetireBlocksAllFourMutations(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	acct, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Guard Target", SubCategoryID: subID}, 1)
	require.NoError(t, err)

	const slotKey = "default_suspense"
	origAccountID := slotAccountID(t, ctx, pool, slotKey)
	t.Cleanup(func() { restoreSlot(t, ctx, pool, slotKey, origAccountID) })

	_, err = RepointSlot(ctx, pool, slotKey, acct.ID, 1)
	require.NoError(t, err)

	t.Run("deactivate blocked", func(t *testing.T) {
		_, err := Update(ctx, pool, cipher, acct.ID, UpdateInput{IsActive: boolPtr(false)}, 1)
		requireBlockedBySlot(t, err, slotKey)
	})
	t.Run("hide blocked", func(t *testing.T) {
		_, err := Update(ctx, pool, cipher, acct.ID, UpdateInput{IsVisible: boolPtr(false)}, 1)
		requireBlockedBySlot(t, err, slotKey)
	})
	t.Run("unpost blocked", func(t *testing.T) {
		_, err := Update(ctx, pool, cipher, acct.ID, UpdateInput{IsPostable: boolPtr(false)}, 1)
		requireBlockedBySlot(t, err, slotKey)
	})
	t.Run("delete blocked", func(t *testing.T) {
		err := SoftDelete(ctx, pool, acct.ID, 1)
		requireBlockedBySlot(t, err, slotKey)
	})

	got, err := Get(ctx, pool, acct.ID)
	require.NoError(t, err)
	assert.True(t, got.IsActive, "every attempted mutation above must have rolled back")
}

// TestConcurrentRepointVsRetire is the Task 15 handoff: RepointSlot pointing a
// slot AT an account races Update retiring that SAME account, in two real
// connections. Both paths lock the account row (RepointSlot locks its target;
// Update locks it via loadCurrent's FOR UPDATE), so Postgres serializes them --
// the assertion is not that the lock exists, but that the invariant it
// protects always holds: no live slot ever ends up wired to an inactive
// account, regardless of which side wins the race.
//
// The loop is not padding. The TOCTOU window this closes is narrow, so a single
// pass proves nothing: with RepointSlot's FOR UPDATE deliberately removed, this
// test passed 10 consecutive races and only failed once the race was repeated
// into the hundreds. lockRaceIterations is calibrated against that measurement,
// not chosen for symmetry.
func TestConcurrentRepointVsRetire(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	const slotKey = "default_bank_charges"
	origAccountID := slotAccountID(t, ctx, pool, slotKey)
	t.Cleanup(func() { restoreSlot(t, ctx, pool, slotKey, origAccountID) })

	for i := 0; i < lockRaceIterations; i++ {
		// A fresh target every iteration: the previous one is left either
		// inactive or wired to the slot, and reusing it would make the second
		// race a no-op that can never deadlock or diverge. Each one is purged
		// at the end of the iteration -- sub-category 1100 only has a hundred
		// codes, so without that this loop exhausts the range and every later
		// Create fails with "No account codes remain".
		target, err := Create(ctx, pool, cipher,
			CreateInput{Name: "T17 Concurrent Target", SubCategoryID: subID}, 1)
		require.NoError(t, err)
		restoreSlot(t, ctx, pool, slotKey, origAccountID)

		var wg sync.WaitGroup
		var repointErr, updateErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, repointErr = RepointSlot(ctx, pool, slotKey, target.ID, 1)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, updateErr = Update(ctx, pool, cipher, target.ID, UpdateInput{IsActive: boolPtr(false)}, 1)
		}()
		close(start)
		wg.Wait()

		switch {
		case repointErr == nil && updateErr == nil:
			t.Fatalf("iteration %d: both the repoint and the retire succeeded -- the invariant "+
				"is broken: an inactive account cannot legally be wired to a live slot", i)
		case repointErr != nil && updateErr != nil:
			t.Fatalf("iteration %d: both sides failed: repoint=%v update=%v", i, repointErr, updateErr)
		case repointErr != nil:
			_, ok := IsConflict(repointErr)
			require.True(t, ok, "iteration %d: the repoint must fail with a ConflictError "+
				"(inactive target), got %v", i, repointErr)
		default:
			_, ok := IsConflict(updateErr)
			require.True(t, ok, "iteration %d: the retire must fail with guardRetire's "+
				"ConflictError, got %v", i, updateErr)
		}

		// Re-verify the invariant directly rather than trusting the error shape
		// alone: whatever the slot points at now must be active.
		requireSlotPointsAtActiveAccount(t, ctx, pool, slotKey)

		// Unwire before purging: the slot may be pointing at this target, and
		// coa_default_mapping holds a foreign key to it.
		restoreSlot(t, ctx, pool, slotKey, origAccountID)
		purgeAccountByUUID(t, ctx, pool, target.ID)
	}
}

// TestConcurrentBulkUpdateOppositeCaseNoDeadlock is the mixed-case race
// lockOrderedUUIDs (store_bulk.go) exists to prevent: two BulkUpdate calls
// naming the same two accounts in opposite order AND opposite letter case. The
// old code passed the same-case version of this race and still deadlocked
// (SQLSTATE 40P01) on the mixed-case one, because Go's byte sort and
// Postgres's case-insensitive uuid comparison disagreed on the lock order.
func TestConcurrentBulkUpdateOppositeCaseNoDeadlock(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	// The uuids must be CHOSEN, not whatever Create happened to generate. Hex
	// digits sort below hex letters in both cases (0x30-0x39 < 0x41-0x46 <
	// 0x61-0x66), so uppercasing a whole set is order-preserving: two random
	// uuids compared as uniformly-uppercase sort exactly as they do as
	// uniformly-lowercase. A uniformly-cased request therefore cannot diverge
	// no matter what ids it draws. Divergence needs a request that MIXES case
	// across ids, which is what request 2 below does.
	p := seedAccountWithUUID(t, ctx, pool, cipher, subID, "T17 Bulk P", lockRaceLowUUID)
	q := seedAccountWithUUID(t, ctx, pool, cipher, subID, "T17 Bulk Q", lockRaceHighUUID)

	// Sorted on the raw client strings these diverge: request 1 gives
	// [aaaa..., bbbb...]; request 2 gives ["BBBB..." (0x42), "aaaa..." (0x61)],
	// i.e. q before p. Opposite lock orders on the same two rows is exactly the
	// cycle Postgres aborts with 40P01. Lowercasing first collapses both to the
	// same order.
	req1 := []string{p, q}
	req2 := []string{strings.ToUpper(q), p}

	// Both requests set is_active to its current value, so no row actually
	// changes -- but loadCurrent still takes FOR UPDATE on every row before the
	// no-op is detected, which is all the lock cycle needs. Keeping the
	// transactions side-effect-free lets this repeat cheaply, and repetition is
	// required: a deadlock needs the two transactions to genuinely interleave,
	// so a single pass proves nothing either way.
	for i := 0; i < lockRaceIterations; i++ {
		var wg sync.WaitGroup
		var err1, err2 error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err1 = BulkUpdate(ctx, pool, BulkInput{UUIDs: req1, IsActive: boolPtr(true)}, 1)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err2 = BulkUpdate(ctx, pool, BulkInput{UUIDs: req2, IsActive: boolPtr(true)}, 1)
		}()
		close(start)
		wg.Wait()

		for _, err := range []error{err1, err2} {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				require.NotEqual(t, "40P01", pgErr.Code,
					"iteration %d: deadlock -- lock ordering regression in lockOrderedUUIDs: %v", i, err)
			}
		}
		// Neither account is wired to a default slot, so there is no legitimate
		// business conflict either: both calls must simply succeed.
		require.NoError(t, err1, "iteration %d", i)
		require.NoError(t, err2, "iteration %d", i)
	}
}

// TestBulkActivateUnhidesHiddenAccount: activating a hidden account through
// the bulk path must implicitly un-hide it (AD-8) and record the actionShow
// history row. Previously this 409'd the whole batch with the message for the
// opposite transition.
func TestBulkActivateUnhidesHiddenAccount(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	acct, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Hidden", SubCategoryID: subID}, 1)
	require.NoError(t, err)
	_, err = Update(ctx, pool, cipher, acct.ID, UpdateInput{IsActive: boolPtr(false)}, 1)
	require.NoError(t, err)
	_, err = Update(ctx, pool, cipher, acct.ID, UpdateInput{IsVisible: boolPtr(false)}, 1)
	require.NoError(t, err)

	results, err := BulkUpdate(ctx, pool, BulkInput{UUIDs: []string{acct.ID}, IsActive: boolPtr(true)}, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Changed, "the activation must be reported as a real change")

	got, err := Get(ctx, pool, acct.ID)
	require.NoError(t, err)
	assert.True(t, got.IsActive)
	assert.True(t, got.IsVisible, "activating a hidden account must implicitly un-hide it")

	var action string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT history_action FROM coa_account_history
		WHERE coa_account_id = (SELECT coa_account_id FROM coa_account WHERE coa_account_uuid = $1)
		  AND history_field = 'is_visible'
		ORDER BY history_at DESC, coa_account_history_id DESC LIMIT 1`, acct.ID).Scan(&action))
	assert.Equal(t, actionShow, action)
}

// TestBulkUpdateAllOrNothing: a batch with one blocked account must leave
// every account in the batch unchanged, never a partial apply.
func TestBulkUpdateAllOrNothing(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	good, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Bulk Good", SubCategoryID: subID}, 1)
	require.NoError(t, err)

	var arUUID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_uuid FROM coa_account WHERE coa_account_code = '1120'`).Scan(&arUUID))

	_, err = BulkUpdate(ctx, pool, BulkInput{
		UUIDs: []string{good.ID, arUUID}, IsActive: boolPtr(false),
	}, 1)
	require.Error(t, err)
	_, ok := IsConflict(err)
	assert.True(t, ok)

	gotGood, err := Get(ctx, pool, good.ID)
	require.NoError(t, err)
	assert.True(t, gotGood.IsActive, "the unblocked account in the batch must not have changed")

	gotAR, err := Get(ctx, pool, arUUID)
	require.NoError(t, err)
	assert.True(t, gotAR.IsActive)
}
