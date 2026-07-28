//go:build dbtest

package chartofaccounts

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/secret"
)

// testPool connects to TEST_DATABASE_URL, or skips the test cleanly when it is
// unset -- the same convention as vendors/store_test.go:18-20, so CI without a
// database stays green.
func testPool(t *testing.T) *pgxpool.Pool {
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

// subcategoryID resolves a seeded sub-category code to its internal id, for
// tests that need to place a new account somewhere real.
func subcategoryID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code int) int {
	t.Helper()
	var id int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code=$1`, code).Scan(&id))
	return id
}

// slotAccountID reads the account a default slot currently points at, so a
// test that repoints it can restore the original wiring afterwards.
func slotAccountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slotKey string) *int {
	t.Helper()
	var id *int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_id FROM coa_default_mapping WHERE slot_key=$1`, slotKey).Scan(&id))
	return id
}

// restoreSlot points slotKey back at accountID via raw SQL, bypassing
// RepointSlot's guard -- this is fixture cleanup, not a mutation under test.
func restoreSlot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slotKey string, accountID *int) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`UPDATE coa_default_mapping SET coa_account_id = $2 WHERE slot_key = $1`, slotKey, accountID)
	require.NoError(t, err)
}

// Fixtures for the bulk lock-ordering race. The uuids are chosen so that
// sorting the raw client strings diverges once one request mixes case:
// 'B' is 0x42 and 'a' is 0x61, so ["BBBB...","aaaa..."] sorts high-then-low
// while ["aaaa...","bbbb..."] sorts low-then-high.
const (
	lockRaceLowUUID    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01"
	lockRaceHighUUID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb02"
	lockRaceIterations = 200
)

// seedAccountWithUUID creates an account and forces its public uuid to a known
// value, returning that uuid. Create assigns a random uuid, and a lock-ordering
// test cannot depend on whatever it happens to draw.
func seedAccountWithUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	cipher *secret.Cipher, subCategoryID int, name, uuid string) string {
	t.Helper()
	// The uuid is fixed, so a leftover row from an earlier run would collide.
	// Hard-delete rather than soft-delete: the unique index is partial on
	// deleted_at IS NULL for the CODE, but coa_account_uuid is unique outright.
	purgeAccountByUUID(t, ctx, pool, uuid)
	acct, err := Create(ctx, pool, cipher, CreateInput{Name: name, SubCategoryID: subCategoryID}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE coa_account SET coa_account_uuid = $2 WHERE coa_account_uuid = $1`, acct.ID, uuid)
	require.NoError(t, err)
	t.Cleanup(func() { purgeAccountByUUID(t, ctx, pool, uuid) })
	return uuid
}

// purgeAccountByUUID hard-deletes an account and its history rows. Fixture
// teardown only -- production code never hard-deletes an account.
func purgeAccountByUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, uuid string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		DELETE FROM coa_account_history
		WHERE coa_account_id IN (SELECT coa_account_id FROM coa_account WHERE coa_account_uuid = $1)`, uuid)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM coa_account WHERE coa_account_uuid = $1`, uuid)
	require.NoError(t, err)
}

// requireSlotPointsAtActiveAccount asserts AD-7's invariant directly against
// the database: whatever slotKey points at, if anything, must still be active.
func requireSlotPointsAtActiveAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slotKey string) {
	t.Helper()
	var pointedID *int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_id FROM coa_default_mapping WHERE slot_key = $1`, slotKey).Scan(&pointedID))
	if pointedID == nil {
		return
	}
	var active bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_is_active FROM coa_account WHERE coa_account_id = $1`, *pointedID).Scan(&active))
	require.True(t, active, "invariant violated: slot %q points at an inactive account", slotKey)
}

// requireBlockedBySlot asserts err is the ConflictError guardRetire raises
// when a default slot still points at the account (AD-7).
func requireBlockedBySlot(t *testing.T, err error, slotKey string) {
	t.Helper()
	require.Error(t, err)
	conflict, ok := IsConflict(err)
	require.True(t, ok, "want a ConflictError, got %v", err)
	assert.Contains(t, conflict.BlockingSlots, slotKey)
}

// TestSeedCounts pins the seed's shape: 9 categories, 17 sub-categories, 127
// system accounts, 19 default slots. Filtered to is_system so this stays
// order-independent alongside sibling tests that insert their own fixtures --
// no seeded row is ever soft-deletable (TestSoftDeleteBlocksSystemAccounts),
// so the system count never drifts.
func TestSeedCounts(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	var cats, subs, accts, slots int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM lkp_coa_category),
		       (SELECT count(*) FROM lkp_coa_subcategory),
		       (SELECT count(*) FROM coa_account WHERE coa_account_is_system),
		       (SELECT count(*) FROM coa_default_mapping)`).Scan(&cats, &subs, &accts, &slots))
	assert.Equal(t, 9, cats)
	assert.Equal(t, 17, subs)
	assert.Equal(t, 127, accts)
	assert.Equal(t, 19, slots)
}

// TestSystemSubCategoryMixesBSAndPNL is AD-2's whole reason for being: 9100 is
// the one sub-category whose seeded accounts span both sides of the balance
// sheet/P&L divide (9101/9102 are BS, 9103-9107 are PNL), which is exactly why
// bs_pnl lives on the account, not the category.
func TestSystemSubCategoryMixesBSAndPNL(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT a.coa_account_bs_pnl FROM coa_account a
		JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
		WHERE s.subcategory_code = 9100 AND a.coa_account_is_system`)
	require.NoError(t, err)
	defer rows.Close()

	var sides []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		sides = append(sides, s)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []string{"BS", "PNL"}, sides)
}

// TestSoftDeleteWithUnresolvedActor is the Critical regression: resolveEmployeeID
// returns 0 for every current caller (employee.employee_user_id is never
// populated), which nullableInt(0) turns into SQL NULL for deleted_by. Before
// 9506209 this was a guaranteed 500 for every soft delete in the system.
//
// The unresolved actor now falls back to the seeded system employee, matching
// every other module (see actorOrSystem). chk_coa_soft_delete stays relaxed --
// it accepts either shape -- but the value this module writes is no longer the
// odd one out.
func TestSoftDeleteWithUnresolvedActor(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	acct, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Delete Me", SubCategoryID: subID}, 1)
	require.NoError(t, err)

	require.NoError(t, SoftDelete(ctx, pool, acct.ID, 0),
		"SoftDelete must succeed when the actor cannot be resolved to an employee row")

	_, err = Get(ctx, pool, acct.ID)
	assert.ErrorIs(t, err, ErrNotFound, "the account must no longer be a live read")

	var deletedBy *int
	var deletedAt *time.Time
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT coa_account_deleted_by, coa_account_deleted_at
		FROM coa_account WHERE coa_account_uuid = $1`, acct.ID).Scan(&deletedBy, &deletedAt))
	require.NotNil(t, deletedBy, "deleted_by must fall back to the system employee, not NULL")
	assert.Equal(t, systemEmployeeID, *deletedBy,
		"an unresolved actor must be recorded as the seeded system employee")
	assert.NotNil(t, deletedAt)
}

// TestSoftDeleteConstraintRejectsDeletedByWithoutDeletedAt covers the half of
// chk_coa_soft_delete that was kept: a row may never claim a deleter without
// also being marked deleted.
func TestSoftDeleteConstraintRejectsDeletedByWithoutDeletedAt(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	acct, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Bad Delete Row", SubCategoryID: subID}, 1)
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`UPDATE coa_account SET coa_account_deleted_by = 1 WHERE coa_account_uuid = $1`, acct.ID)
	require.Error(t, err, "chk_coa_soft_delete must reject a deleter claimed without a deleted_at")
}

// TestSoftDeleteBlocksSystemAccounts proves the seeded-account guard renders a
// 409 ConflictError, not chk_coa_system_undeletable surfacing as a raw
// constraint violation. Every seeded account is is_system, so 1130 (Employee
// Advances, unused by any default slot) exercises exactly this guard alone.
func TestSoftDeleteBlocksSystemAccounts(t *testing.T) {
	pool, ctx := testPool(t), context.Background()

	var seededUUID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_uuid FROM coa_account WHERE coa_account_code = '1130'`).Scan(&seededUUID))

	err := SoftDelete(ctx, pool, seededUUID, 1)
	require.Error(t, err)
	_, ok := IsConflict(err)
	assert.True(t, ok, "a seeded account must fail with a 409 ConflictError, not a raw DB constraint error")

	got, err := Get(ctx, pool, seededUUID)
	require.NoError(t, err, "the seeded account must remain live")
	assert.True(t, got.IsSystem)
}

// TestSoftDeleteBlockedByLiveChildren exercises the non-zero branch of
// hasLiveChildren, which no Go test had reached before: a parent stays
// undeletable until every live child is gone, then becomes deletable.
func TestSoftDeleteBlockedByLiveChildren(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	parent, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Parent", SubCategoryID: subID}, 1)
	require.NoError(t, err)
	child, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Child", ParentID: parent.ID}, 1)
	require.NoError(t, err)

	err = SoftDelete(ctx, pool, parent.ID, 1)
	require.Error(t, err)
	_, ok := IsConflict(err)
	assert.True(t, ok, "a parent with a live child must not be deletable")

	require.NoError(t, SoftDelete(ctx, pool, child.ID, 1))
	require.NoError(t, SoftDelete(ctx, pool, parent.ID, 1),
		"once the child is gone the parent becomes deletable")
}
