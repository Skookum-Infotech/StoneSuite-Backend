//go:build dbtest

package workflow

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPool connects to TEST_DATABASE_URL, skipping the test cleanly when it
// is not set (mirrors the pattern used by other packages' dbtest helpers,
// e.g. journal/store_dbtest_test.go).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestDocumentSends_InsertAndList(t *testing.T) {
	pool := testPool(t) // existing dbtest helper in the workflow package
	ctx := context.Background()
	recordID := "11111111-1111-1111-1111-111111111111"

	id, err := InsertDocumentSend(ctx, pool, DocumentSend{
		RecordID:              recordID,
		WorkflowKey:           "invoice",
		SentTo:                "bob@buyer.example",
		Subject:               "Your Invoice INV-1001",
		NotifyNotificationIDs: []string{"notif-1", "notif-2"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	list, err := ListDocumentSends(ctx, pool, recordID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "invoice", list[0].WorkflowKey)
	assert.Equal(t, "bob@buyer.example", list[0].SentTo)
	assert.Equal(t, []string{"notif-1", "notif-2"}, list[0].NotifyNotificationIDs)

	// A send recorded with no notify ids round-trips as an empty slice
	// (the column DEFAULT '{}'), never a scan error.
	other := "22222222-2222-2222-2222-222222222222"
	_, err = InsertDocumentSend(ctx, pool, DocumentSend{RecordID: other, WorkflowKey: "quote", SentTo: "x@y.z"})
	require.NoError(t, err)
	list, err = ListDocumentSends(ctx, pool, other)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Empty(t, list[0].NotifyNotificationIDs)
}
