package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Document send attempt statuses (document_send_attempt.status).
const (
	// SendAttemptPending marks a send that is in flight (or crashed mid-flight).
	SendAttemptPending = "pending"
	// SendAttemptSent marks a send that was queued and recorded in document_sends.
	SendAttemptSent = "sent"
	// SendAttemptFailed marks a send that did not complete and may be reclaimed.
	SendAttemptFailed = "failed"
)

// DocumentSendAttempt is one row of document_send_attempt: the intent to email
// a document, written before the email is queued so retries can be deduped.
type DocumentSendAttempt struct {
	ID                    string
	RecordID              string
	WorkflowKey           string
	IdempotencyKey        string
	RequestFingerprint    string
	Status                string
	SentByUserID          string
	NotifyNotificationIDs []string
}

const documentSendAttemptCols = `id::text, record_id::text, workflow_key, idempotency_key, request_fingerprint,
	status, COALESCE(sent_by_user_id::text, ''), notify_notification_ids`

func scanDocumentSendAttempt(row pgx.Row) (DocumentSendAttempt, error) {
	var a DocumentSendAttempt
	err := row.Scan(&a.ID, &a.RecordID, &a.WorkflowKey, &a.IdempotencyKey, &a.RequestFingerprint,
		&a.Status, &a.SentByUserID, &a.NotifyNotificationIDs)
	if err != nil {
		return DocumentSendAttempt{}, err
	}
	return a, nil
}

// CreateOrGetDocumentSendAttempt inserts a pending attempt, or returns the
// existing one for the same (record, workflow, non-empty idempotency key). The
// bool reports whether a new row was created. An empty key always inserts.
func CreateOrGetDocumentSendAttempt(ctx context.Context, q Querier, in DocumentSendAttempt) (DocumentSendAttempt, bool, error) {
	a, err := scanDocumentSendAttempt(q.QueryRow(ctx, `
		INSERT INTO document_send_attempt
			(record_id, workflow_key, idempotency_key, request_fingerprint, sent_by_user_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (record_id, workflow_key, idempotency_key) WHERE idempotency_key <> '' DO NOTHING
		RETURNING `+documentSendAttemptCols,
		in.RecordID, in.WorkflowKey, in.IdempotencyKey, in.RequestFingerprint, nullIfEmpty(in.SentByUserID)))
	if err == nil {
		return a, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DocumentSendAttempt{}, false, fmt.Errorf("insert document send attempt: %w", err)
	}
	a, err = scanDocumentSendAttempt(q.QueryRow(ctx, `
		SELECT `+documentSendAttemptCols+`
		FROM document_send_attempt
		WHERE record_id = $1 AND workflow_key = $2 AND idempotency_key = $3`,
		in.RecordID, in.WorkflowKey, in.IdempotencyKey))
	if err != nil {
		return DocumentSendAttempt{}, false, fmt.Errorf("select document send attempt: %w", err)
	}
	return a, false, nil
}

// ClaimDocumentSendAttempt atomically (re)takes an attempt for sending: it
// succeeds when the attempt is failed, or pending for longer than staleAfter.
// A false result means another send is in flight (or the attempt is sent).
func ClaimDocumentSendAttempt(ctx context.Context, q Querier, id string, staleAfter time.Duration) (bool, error) {
	var claimed string
	err := q.QueryRow(ctx, `
		UPDATE document_send_attempt
		SET status = 'pending', updated_at = now()
		WHERE id = $1
		  AND (status = 'failed'
		       OR (status = 'pending' AND updated_at < now() - make_interval(secs => $2)))
		RETURNING id::text`, id, staleAfter.Seconds()).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim document send attempt: %w", err)
	}
	return true, nil
}

// MarkDocumentSendAttemptFailed flags a not-yet-sent attempt as failed so a
// retry may reclaim it immediately. A sent attempt is never downgraded.
func MarkDocumentSendAttemptFailed(ctx context.Context, q Querier, id string) error {
	if _, err := q.Exec(ctx, `
		UPDATE document_send_attempt SET status = 'failed', updated_at = now()
		WHERE id = $1 AND status <> 'sent'`, id); err != nil {
		return fmt.Errorf("mark document send attempt failed: %w", err)
	}
	return nil
}

// FinalizeDocumentSend records the send in document_sends (using the attempt's
// id as the row id) and flips the attempt to sent, in one transaction. s.ID is
// ignored; attemptID is used.
func FinalizeDocumentSend(ctx context.Context, b Beginner, attemptID string, s DocumentSend) error {
	ids := s.NotifyNotificationIDs
	if ids == nil {
		ids = []string{}
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin finalize document send: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_sends
			(id, record_id, workflow_key, attachment_id, sent_to, cc, subject, sent_by_user_id, notify_notification_ids)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO NOTHING`,
		attemptID, s.RecordID, s.WorkflowKey, nullIfEmpty(s.AttachmentID), s.SentTo, s.CC, s.Subject,
		nullIfEmpty(s.SentByUserID), ids); err != nil {
		return fmt.Errorf("insert document send: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE document_send_attempt
		SET status = 'sent', notify_notification_ids = $2, updated_at = now()
		WHERE id = $1`, attemptID, ids); err != nil {
		return fmt.Errorf("mark document send attempt sent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit finalize document send: %w", err)
	}
	return nil
}
