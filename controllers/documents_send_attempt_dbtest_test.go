//go:build dbtest

package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

// dedupingNotify dedupes by IdempotencyKey+recipient like notify does, records each
// real delivery, and can fail the first call with a timeout AFTER delivering.
type dedupingNotify struct {
	mu          sync.Mutex
	calls       int
	failFirst   bool
	seen        map[string]string
	deliveries  map[string]int
	lastIdemKey string
}

func newDedupingNotify(failFirst bool) *dedupingNotify {
	return &dedupingNotify{failFirst: failFirst, seen: map[string]string{}, deliveries: map[string]int{}}
}

func (f *dedupingNotify) send(_ context.Context, req services.NotificationRequest) (services.NotificationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastIdemKey = req.IdempotencyKey
	var res services.NotificationResult
	for _, r := range req.Recipients {
		k := req.IdempotencyKey + "|" + r.Email
		id, dup := f.seen[k]
		if !dup {
			id = fmt.Sprintf("n-%d", len(f.seen)+1)
			f.seen[k] = id
			f.deliveries[r.Email]++
		}
		res.NotificationIDs = append(res.NotificationIDs, id)
	}
	if f.failFirst && f.calls == 1 {
		return res, errors.New("context deadline exceeded")
	}
	return res, nil
}

func TestRunSendAttempt_RetryAfterTimeoutDeliversOnce(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	recordID := newTestUUID(t, pool)
	const wf = "invoice"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM document_sends WHERE record_id = $1`, recordID)
		_, _ = pool.Exec(ctx, `DELETE FROM document_send_attempt WHERE record_id = $1`, recordID)
	})

	userID := newSendTestUser(t, pool)
	fake := newDedupingNotify(true)
	attempt := func(fp string) workflow.DocumentSendAttempt {
		return workflow.DocumentSendAttempt{RecordID: recordID, WorkflowKey: wf, IdempotencyKey: "key-1", RequestFingerprint: fp, SentByUserID: userID}
	}
	record := workflow.DocumentSend{RecordID: recordID, WorkflowKey: wf, SentTo: "a@x.com, b@x.com", Subject: "S"}
	build := func(string) services.NotificationRequest {
		return services.NotificationRequest{Recipients: []services.RecipientTarget{{Email: "a@x.com"}, {Email: "b@x.com"}}}
	}
	attemptStatus := func() (string, int) {
		var st string
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT status, count(*) OVER () FROM document_send_attempt WHERE record_id = $1`, recordID).Scan(&st, &n))
		return st, n
	}

	// First call: notify delivers then times out -> 502, attempt failed, no send row.
	_, serr := runSendAttempt(ctx, pool, fake.send, attempt("fp"), record, build)
	require.NotNil(t, serr)
	assert.Equal(t, http.StatusBadGateway, serr.Status)
	st, n := attemptStatus()
	assert.Equal(t, workflow.SendAttemptFailed, st)
	assert.Equal(t, 1, n)
	var sends int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_sends WHERE record_id = $1`, recordID).Scan(&sends))
	assert.Zero(t, sends)

	// Retry, same key: same attempt, no duplicate delivery, now sent.
	out, serr := runSendAttempt(ctx, pool, fake.send, attempt("fp"), record, build)
	require.Nil(t, serr)
	assert.False(t, out.Replay)
	assert.Equal(t, 1, fake.deliveries["a@x.com"])
	assert.Equal(t, 1, fake.deliveries["b@x.com"])
	st, n = attemptStatus()
	assert.Equal(t, workflow.SendAttemptSent, st)
	assert.Equal(t, 1, n)
	var sendID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id::text FROM document_sends WHERE record_id = $1`, recordID).Scan(&sendID))
	assert.Equal(t, out.SendID, sendID)
	assert.Equal(t, documentSendNotifyKey(out.SendID), fake.lastIdemKey)

	// Third call replays without notifying.
	callsBefore := fake.calls
	out2, serr := runSendAttempt(ctx, pool, fake.send, attempt("fp"), record, build)
	require.Nil(t, serr)
	assert.True(t, out2.Replay)
	assert.Equal(t, out.SendID, out2.SendID)
	assert.Equal(t, callsBefore, fake.calls)
	assert.Len(t, out2.Notify.NotificationIDs, 2)

	// Same key, different request -> 409.
	_, serr = runSendAttempt(ctx, pool, fake.send, attempt("other-fp"), record, build)
	require.NotNil(t, serr)
	assert.Equal(t, http.StatusConflict, serr.Status)
	assert.Equal(t, msgIdemKeyReused, serr.Msg)
	assert.Equal(t, callsBefore, fake.calls)
}

func TestRunSendAttempt_PendingInFlightConflicts(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	recordID := newTestUUID(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM document_send_attempt WHERE record_id = $1`, recordID)
	})
	in := workflow.DocumentSendAttempt{RecordID: recordID, WorkflowKey: "invoice", IdempotencyKey: "k", RequestFingerprint: "fp", SentByUserID: newSendTestUser(t, pool)}
	_, created, err := workflow.CreateOrGetDocumentSendAttempt(ctx, pool, in)
	require.NoError(t, err)
	require.True(t, created)

	fake := newDedupingNotify(false)
	_, serr := runSendAttempt(ctx, pool, fake.send, in, workflow.DocumentSend{RecordID: recordID, WorkflowKey: "invoice", SentTo: "a@x.com"},
		func(string) services.NotificationRequest { return services.NotificationRequest{} })
	require.NotNil(t, serr)
	assert.Equal(t, http.StatusConflict, serr.Status)
	assert.Equal(t, msgSendInProgress, serr.Msg)
	assert.Zero(t, fake.calls)
}

// TestRunSendAttempt_KeyNeverSharedAcrossUsers covers the sender guard: a
// different user reusing a key gets 409, and a caller with no tenant user never
// matches an existing attempt (its key is dropped, so it sends fresh).
func TestRunSendAttempt_KeyNeverSharedAcrossUsers(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	recordID := newTestUUID(t, pool)
	const wf = "invoice"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM document_sends WHERE record_id = $1`, recordID)
		_, _ = pool.Exec(ctx, `DELETE FROM document_send_attempt WHERE record_id = $1`, recordID)
	})
	record := workflow.DocumentSend{RecordID: recordID, WorkflowKey: wf, SentTo: "a@x.com"}
	build := func(string) services.NotificationRequest {
		return services.NotificationRequest{Recipients: []services.RecipientTarget{{Email: "a@x.com"}}}
	}
	attempt := func(userID string) workflow.DocumentSendAttempt {
		return workflow.DocumentSendAttempt{RecordID: recordID, WorkflowKey: wf, IdempotencyKey: "shared", RequestFingerprint: "fp", SentByUserID: userID}
	}
	fake := newDedupingNotify(false)
	first, serr := runSendAttempt(ctx, pool, fake.send, attempt(newSendTestUser(t, pool)), record, build)
	require.Nil(t, serr)

	tests := []struct {
		name       string
		userID     string
		wantStatus int
	}{
		{name: "other user reusing key conflicts", userID: newSendTestUser(t, pool), wantStatus: http.StatusConflict},
		{name: "no tenant user sends fresh", userID: "", wantStatus: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, serr := runSendAttempt(ctx, pool, fake.send, attempt(tc.userID), record, build)
			if tc.wantStatus != 0 {
				require.NotNil(t, serr)
				assert.Equal(t, tc.wantStatus, serr.Status)
				return
			}
			require.Nil(t, serr)
			assert.False(t, out.Replay)
			assert.NotEqual(t, first.SendID, out.SendID)
		})
	}
}

// newSendTestUser inserts a tenant users row and returns its id.
func newSendTestUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var userID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, full_name)
		 VALUES (gen_random_uuid(), 'send-' || gen_random_uuid()::text || '@example.com', 'Send Test')
		 RETURNING id::text`).Scan(&userID))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM document_sends WHERE sent_by_user_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM document_send_attempt WHERE sent_by_user_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

// newTestUUID returns a fresh random UUID generated by the database.
func newTestUUID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id))
	return id
}
