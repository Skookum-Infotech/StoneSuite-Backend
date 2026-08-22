//go:build dbtest

package customernote

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// seedCustomer inserts a minimal live customer, mirroring crmactivity's
// seedCustomer helper.
func seedCustomer(t *testing.T, pool *pgxpool.Pool) (custUUID string, custID int) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	require.NoError(t, pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid, customer_id`,
		custTypeID, "Test Customer "+suffix).Scan(&custUUID, &custID))
	return custUUID, custID
}

// seedCustomerIdentity inserts an active customer_identities row.
func seedCustomerIdentity(t *testing.T, pool *pgxpool.Pool, custID int) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO customer_identities (customer_id, email, full_name, status)
		VALUES ($1, $2, 'Test Customer', 'active') RETURNING id`,
		custID, fmt.Sprintf("customer-%s@example.com", suffix)).Scan(&id))
	return id
}

func TestCreate_And_ListByCustomerID_DB(t *testing.T) {
	pool := testPool(t)
	_, custID := seedCustomer(t, pool)
	identityID := seedCustomerIdentity(t, pool, custID)

	note, err := Create(context.Background(), pool, custID, identityID, CreateNoteInput{Body: "I have an issue with X."})
	require.NoError(t, err)
	assert.Equal(t, "I have an issue with X.", note.Body)
	assert.Equal(t, "new", note.Status)
	assert.Equal(t, identityID, note.Submitter.ID)

	notes, err := ListByCustomerID(context.Background(), pool, custID)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, note.ID, notes[0].ID)
}

func TestCreate_RejectsEmptyBody_DB(t *testing.T) {
	pool := testPool(t)
	_, custID := seedCustomer(t, pool)
	identityID := seedCustomerIdentity(t, pool, custID)

	_, err := Create(context.Background(), pool, custID, identityID, CreateNoteInput{Body: "   "})
	assert.True(t, IsClientError(err))
}

// TestListByCustomerID_NeverCrossesCustomers is the isolation guarantee the
// customer-portal handlers rely on: a customer's own notes list must never
// include another customer's notes, even within the same tenant database.
func TestListByCustomerID_NeverCrossesCustomers(t *testing.T) {
	pool := testPool(t)
	_, custA := seedCustomer(t, pool)
	identityA := seedCustomerIdentity(t, pool, custA)
	_, custB := seedCustomer(t, pool)
	identityB := seedCustomerIdentity(t, pool, custB)

	_, err := Create(context.Background(), pool, custA, identityA, CreateNoteInput{Body: "Note from A"})
	require.NoError(t, err)
	_, err = Create(context.Background(), pool, custB, identityB, CreateNoteInput{Body: "Note from B"})
	require.NoError(t, err)

	notesA, err := ListByCustomerID(context.Background(), pool, custA)
	require.NoError(t, err)
	require.Len(t, notesA, 1)
	assert.Equal(t, "Note from A", notesA[0].Body)

	notesB, err := ListByCustomerID(context.Background(), pool, custB)
	require.NoError(t, err)
	require.Len(t, notesB, 1)
	assert.Equal(t, "Note from B", notesB[0].Body)
}

func TestListByCustomerRecord_And_UpdateStatus_DB(t *testing.T) {
	pool := testPool(t)
	custUUID, custID := seedCustomer(t, pool)
	identityID := seedCustomerIdentity(t, pool, custID)

	note, err := Create(context.Background(), pool, custID, identityID, CreateNoteInput{Body: "Staff-visible note"})
	require.NoError(t, err)

	notes, err := ListByCustomerRecord(context.Background(), pool, custUUID)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, note.ID, notes[0].ID)

	updated, err := UpdateStatus(context.Background(), pool, custUUID, note.ID, UpdateStatusInput{Status: "resolved"}, 1)
	require.NoError(t, err)
	assert.Equal(t, "resolved", updated.Status)
}

func TestUpdateStatus_RejectsInvalidStatus_DB(t *testing.T) {
	pool := testPool(t)
	custUUID, custID := seedCustomer(t, pool)
	identityID := seedCustomerIdentity(t, pool, custID)
	note, err := Create(context.Background(), pool, custID, identityID, CreateNoteInput{Body: "x"})
	require.NoError(t, err)

	_, err = UpdateStatus(context.Background(), pool, custUUID, note.ID, UpdateStatusInput{Status: "archived"}, 1)
	assert.True(t, IsClientError(err))
}

// TestUpdateStatus_WrongRecordUUID_NotFound confirms verifyBelongsToRecord
// rejects a noteId that belongs to a different CRM record than the one in
// the path — the IDOR guard staff-facing callers rely on.
func TestUpdateStatus_WrongRecordUUID_NotFound(t *testing.T) {
	pool := testPool(t)
	custUUIDA, custIDA := seedCustomer(t, pool)
	identityA := seedCustomerIdentity(t, pool, custIDA)
	custUUIDB, _ := seedCustomer(t, pool)

	note, err := Create(context.Background(), pool, custIDA, identityA, CreateNoteInput{Body: "belongs to A"})
	require.NoError(t, err)

	_, err = UpdateStatus(context.Background(), pool, custUUIDB, note.ID, UpdateStatusInput{Status: "read"}, 1)
	assert.ErrorIs(t, err, ErrNotFound)

	// Sanity: the correct record uuid still works.
	_, err = UpdateStatus(context.Background(), pool, custUUIDA, note.ID, UpdateStatusInput{Status: "read"}, 1)
	assert.NoError(t, err)
}

func TestSoftDelete_DB(t *testing.T) {
	pool := testPool(t)
	custUUID, custID := seedCustomer(t, pool)
	identityID := seedCustomerIdentity(t, pool, custID)
	note, err := Create(context.Background(), pool, custID, identityID, CreateNoteInput{Body: "to be deleted"})
	require.NoError(t, err)

	require.NoError(t, SoftDelete(context.Background(), pool, custUUID, note.ID, 1))

	_, err = Get(context.Background(), pool, note.ID)
	assert.ErrorIs(t, err, ErrNotFound)

	notes, err := ListByCustomerID(context.Background(), pool, custID)
	require.NoError(t, err)
	assert.Empty(t, notes)
}
