//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/recordemail"
)

// saveInternalNote is the whole server side of the Communication tab's "Internal
// only" switch: the note is filed on the record under the signed-in user's name,
// marked internal, with no recipient and no subject, and audit-logged. Nothing
// here can reach the mail provider (the function has no notifier), which is the
// point of the switch.

func newNoteRequest(recordID string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/tenant/records/"+recordID+"/emails", nil)
}

func TestSaveInternalNote_FilesATeamNoteUnderTheUsersName(t *testing.T) {
	ctx, pool := context.Background(), inboundPool(t, "TEST_DATABASE_URL")

	var recordID, identityID, userID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&recordID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&identityID))
	email := fmt.Sprintf("note-%d@example.com", time.Now().UnixNano())
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, full_name) VALUES ($1, $2, 'Harish Narne') RETURNING id::text`,
		identityID, email).Scan(&userID))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM record_email_message WHERE record_id = $1`, recordID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE resource_id = $1`, recordID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	rec := httptest.NewRecorder()
	saveInternalNote(rec, newNoteRequest(recordID), pool, "sales_order", recordID, identityID, "Customer wants granite.", "", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"success":true}`, rec.Body.String())

	msgs, err := recordemail.List(ctx, pool, recordID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	note := msgs[0]
	assert.True(t, note.Internal)
	assert.Equal(t, recordemail.DirectionOut, note.Direction)
	assert.Equal(t, "Customer wants granite.", note.Body)
	assert.Equal(t, "Harish Narne", note.FromName)
	assert.Equal(t, email, note.FromEmail)
	assert.Equal(t, userID, note.SentByUserID)
	assert.Empty(t, note.To, "a note is never addressed to anyone")
	assert.Empty(t, note.Subject)

	// A note alone gives the thread nothing to reply to or to answer.
	target, err := recordemail.ReplyTarget(ctx, pool, recordID)
	require.NoError(t, err)
	assert.Empty(t, target)
	latest, err := recordemail.Latest(ctx, pool, recordID)
	require.NoError(t, err)
	assert.Nil(t, latest)

	var audited int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE resource_id = $1 AND action = 'record_email.note_added'`,
		recordID).Scan(&audited))
	assert.Equal(t, 1, audited, "one audit entry per note")
}

func TestSaveInternalNote_StillSavesWhenTheUserCannotBeResolved(t *testing.T) {
	ctx, pool := context.Background(), inboundPool(t, "TEST_DATABASE_URL")

	var recordID, unknownIdentity string
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&recordID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&unknownIdentity))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM record_email_message WHERE record_id = $1`, recordID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE resource_id = $1`, recordID)
	})

	rec := httptest.NewRecorder()
	saveInternalNote(rec, newNoteRequest(recordID), pool, "sales_order", recordID, unknownIdentity, "Left a voicemail.", "", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	msgs, err := recordemail.List(ctx, pool, recordID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].Internal)
	assert.Equal(t, "Left a voicemail.", msgs[0].Body)
	assert.Empty(t, msgs[0].FromName, "the tab falls back to a neutral label for an unknown author")
	assert.Empty(t, msgs[0].SentByUserID)
}
