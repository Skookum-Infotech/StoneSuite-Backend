package recordemail

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// execer is what an insert needs: satisfied by a pool and by a transaction, so one
// statement serves a lone message and a message written together with its files.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// messageColumns is the column list every read selects, in scanMessage's order.
// Nullable columns are coalesced so a Message never carries a NULL.
const messageColumns = `id, record_id, workflow_key, direction, from_name, from_email,
	to_emails, cc_emails, subject, body_text, message_id, in_reply_to, references_header,
	provider_email_id, attachment_count, COALESCE(sent_by_user_id::text, ''),
	COALESCE(document_send_id::text, ''), internal, created_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	var direction string
	err := row.Scan(&m.ID, &m.RecordID, &m.WorkflowKey, &direction, &m.FromName, &m.FromEmail,
		&m.To, &m.CC, &m.Subject, &m.Body, &m.MessageID, &m.InReplyTo, &m.References,
		&m.ProviderEmailID, &m.AttachmentCount, &m.SentByUserID, &m.DocumentSendID, &m.Internal, &m.CreatedAt)
	m.Direction = Direction(direction)
	return m, err
}

// Insert stores m in the tenant database. It returns false (and no error) when
// the message was already there: a webhook retry carries the same provider email
// id / Message-ID, and must not add a second copy. m.ID is normally empty and
// the database picks one; a caller that attaches files to the message in the same
// breath sets it first, so it knows the id without reading the row back.
func Insert(ctx context.Context, pool *pgxpool.Pool, m Message) (bool, error) {
	return insertMessage(ctx, pool, m)
}

// InsertWithFiles stores m and the files kept for it in one transaction, so a
// message never exists with only some of its files recorded. Every Attachment
// needs its MessageID (m.ID), RecordID, StorageKey and Status set.
func InsertWithFiles(ctx context.Context, pool *pgxpool.Pool, m Message, atts []Attachment) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin record email insert for %s: %w", m.RecordID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := insertMessage(ctx, tx, m); err != nil {
		return err
	}
	if err := insertAttachmentRows(ctx, tx, atts); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit record email insert for %s: %w", m.RecordID, err)
	}
	return nil
}

func insertMessage(ctx context.Context, db execer, m Message) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO record_email_message
			(id, record_id, workflow_key, direction, from_name, from_email, to_emails, cc_emails,
			 subject, body_text, message_id, in_reply_to, references_header, provider_email_id,
			 attachment_count, sent_by_user_id, document_send_id, internal)
		VALUES (COALESCE(NULLIF($18, '')::uuid, gen_random_uuid()),
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			NULLIF($15, '')::uuid, NULLIF($16, '')::uuid, $17)
		ON CONFLICT DO NOTHING`,
		m.RecordID, m.WorkflowKey, string(m.Direction), m.FromName, m.FromEmail, m.To, m.CC,
		m.Subject, m.Body, m.MessageID, m.InReplyTo, m.References, m.ProviderEmailID,
		m.AttachmentCount, m.SentByUserID, m.DocumentSendID, m.Internal, m.ID)
	if err != nil {
		return false, fmt.Errorf("insert record email message for %s: %w", m.RecordID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// List returns a record's conversation, oldest first, each message with the files
// kept for it.
func List(ctx context.Context, pool *pgxpool.Pool, recordID string) ([]Message, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+messageColumns+` FROM record_email_message WHERE record_id = $1 ORDER BY created_at, id`,
		recordID)
	if err != nil {
		return nil, fmt.Errorf("list record email messages for %s: %w", recordID, err)
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan record email message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list record email messages for %s: %w", recordID, err)
	}
	rows.Close()
	if err := attachFiles(ctx, pool, recordID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Latest returns the record's most recent real email, or (nil, nil) when it has
// none. A reply is threaded under it, so an internal note is never the latest:
// it has no subject to answer.
func Latest(ctx context.Context, pool *pgxpool.Pool, recordID string) (*Message, error) {
	m, err := scanMessage(pool.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM record_email_message WHERE record_id = $1 AND NOT internal
		 ORDER BY created_at DESC, id DESC LIMIT 1`, recordID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest record email message for %s: %w", recordID, err)
	}
	return &m, nil
}

// ReplyTarget returns the address a staff reply should go to: whoever wrote the
// most recent customer message, else the first recipient of the first mail we
// sent. "" when the record has no messages at all.
func ReplyTarget(ctx context.Context, pool *pgxpool.Pool, recordID string) (string, error) {
	var from string
	err := pool.QueryRow(ctx, `
		SELECT from_email FROM record_email_message
		WHERE record_id = $1 AND direction = 'in' AND from_email <> ''
		ORDER BY created_at DESC, id DESC LIMIT 1`, recordID).Scan(&from)
	if err == nil {
		return from, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("reply target for %s: %w", recordID, err)
	}
	var to string
	err = pool.QueryRow(ctx, `
		SELECT to_emails FROM record_email_message
		WHERE record_id = $1 AND direction = 'out' AND NOT internal AND to_emails <> ''
		ORDER BY created_at, id LIMIT 1`, recordID).Scan(&to)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reply target for %s: %w", recordID, err)
	}
	return FirstAddress(to), nil
}

// InboundMessageIDs returns the Message-IDs of the record's inbound mail, oldest
// first, for an outgoing References header. Only inbound ones are known: the
// mail provider assigns the Message-ID of what we send.
func InboundMessageIDs(ctx context.Context, pool *pgxpool.Pool, recordID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT message_id FROM record_email_message
		WHERE record_id = $1 AND direction = 'in' AND message_id <> ''
		ORDER BY created_at, id`, recordID)
	if err != nil {
		return nil, fmt.Errorf("inbound message ids for %s: %w", recordID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan inbound message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
