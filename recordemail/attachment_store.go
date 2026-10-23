package recordemail

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAttachmentNotFound means no file with that id belongs to the record.
var ErrAttachmentNotFound = errors.New("email attachment not found")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// attachmentColumns is the column list every read selects, in scanAttachment's
// order. A pending file older than PendingExpiry reads as unavailable (the second
// bind parameter is that cutoff), so a copy that never finished is not shown as
// still in progress.
const attachmentColumns = `id::text, message_id::text, record_id::text, file_name, content_type,
	size_bytes, is_inline, provider_attachment_id, storage_key,
	CASE WHEN status = 'pending' AND created_at < $2 THEN 'unavailable' ELSE status END`

func scanAttachment(row pgx.Row) (Attachment, error) {
	var a Attachment
	err := row.Scan(&a.ID, &a.MessageID, &a.RecordID, &a.FileName, &a.ContentType,
		&a.SizeBytes, &a.Inline, &a.ProviderAttachmentID, &a.StorageKey, &a.Status)
	return a, err
}

func pendingCutoff() time.Time { return time.Now().Add(-PendingExpiry) }

// attachFiles fills in the Attachments of msgs (all from recordID) with one query.
// A message with no files gets an empty list, never nil, so it reads as [] in JSON.
func attachFiles(ctx context.Context, pool *pgxpool.Pool, recordID string, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	rows, err := pool.Query(ctx,
		`SELECT `+attachmentColumns+` FROM record_email_attachment WHERE record_id = $1 ORDER BY created_at, id`,
		recordID, pendingCutoff())
	if err != nil {
		return fmt.Errorf("list email attachments for %s: %w", recordID, err)
	}
	defer rows.Close()

	byMessage := map[string][]Attachment{}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return fmt.Errorf("scan email attachment: %w", err)
		}
		byMessage[a.MessageID] = append(byMessage[a.MessageID], a)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list email attachments for %s: %w", recordID, err)
	}
	for i := range msgs {
		msgs[i].Attachments = byMessage[msgs[i].ID]
		if msgs[i].Attachments == nil {
			msgs[i].Attachments = []Attachment{}
		}
	}
	return nil
}

// FileReceived records the files that arrived with a mail, under the message that
// mail was filed as, and returns the provider ids of the ones still waiting to be
// copied. It is safe to call again for a retried delivery: a file already recorded
// is left as it is, and is returned again while it is still pending, so a retry
// picks up the copies an earlier attempt did not finish.
//
// canStore says whether the tenant can keep files at all; without it nothing is
// pending. A mail whose message is not found under providerEmailID (it was filed
// by an earlier delivery with another id) has nothing to attach to: no files.
func FileReceived(
	ctx context.Context, pool *pgxpool.Pool, recordID, providerEmailID string, files []ReceivedFile, canStore bool,
) ([]string, error) {
	pending := []string{}
	if len(files) == 0 {
		return pending, nil
	}
	var messageID string
	err := pool.QueryRow(ctx,
		`SELECT id::text FROM record_email_message WHERE record_id = $1 AND provider_email_id = $2`,
		recordID, providerEmailID).Scan(&messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pending, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find message %s for its attachments: %w", providerEmailID, err)
	}

	for _, f := range files {
		_, err := pool.Exec(ctx, `
			INSERT INTO record_email_attachment
				(message_id, record_id, file_name, content_type, size_bytes, is_inline, provider_attachment_id, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (message_id, provider_attachment_id) WHERE provider_attachment_id <> '' DO NOTHING`,
			messageID, recordID, receivedFileName(f.Name), f.ContentType, max(f.Size, 0), f.Inline, f.ProviderID,
			ClassifyReceived(f.Name, f.Size, canStore))
		if err != nil {
			return nil, fmt.Errorf("record received attachment %s: %w", f.ProviderID, err)
		}
	}

	rows, err := pool.Query(ctx, `
		SELECT provider_attachment_id FROM record_email_attachment
		WHERE message_id = $1 AND status = $2 AND provider_attachment_id <> ''
		ORDER BY created_at, id`, messageID, AttachmentPending)
	if err != nil {
		return nil, fmt.Errorf("list pending attachments of %s: %w", messageID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan pending attachment: %w", err)
		}
		pending = append(pending, id)
	}
	return pending, rows.Err()
}

// insertAttachmentRows writes the files kept for a message (the ones staff
// attached to a reply). See InsertWithFiles.
func insertAttachmentRows(ctx context.Context, db execer, atts []Attachment) error {
	for _, a := range atts {
		_, err := db.Exec(ctx, `
			INSERT INTO record_email_attachment
				(message_id, record_id, file_name, content_type, size_bytes, is_inline, storage_key, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			a.MessageID, a.RecordID, a.FileName, a.ContentType, a.SizeBytes, a.Inline, a.StorageKey, a.Status)
		if err != nil {
			return fmt.Errorf("insert email attachment %s: %w", a.FileName, err)
		}
	}
	return nil
}

// GetAttachment returns one file of the record's conversation, or
// ErrAttachmentNotFound when the id is not a file of that record (or not an id).
func GetAttachment(ctx context.Context, pool *pgxpool.Pool, recordID, attachmentID string) (Attachment, error) {
	if !uuidPattern.MatchString(attachmentID) {
		return Attachment{}, ErrAttachmentNotFound
	}
	a, err := scanAttachment(pool.QueryRow(ctx,
		`SELECT `+attachmentColumns+` FROM record_email_attachment WHERE record_id = $1::uuid AND id = $3::uuid`,
		recordID, pendingCutoff(), attachmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrAttachmentNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get email attachment %s: %w", attachmentID, err)
	}
	return a, nil
}

// ReceivedAttachment returns the record's file that the mail provider knows as
// providerID, or ErrAttachmentNotFound. The state is the stored one, not the
// presented one: a copy that finishes late must still be accepted.
func ReceivedAttachment(ctx context.Context, pool *pgxpool.Pool, recordID, providerID string) (Attachment, error) {
	// A zero cutoff keeps the CASE from ever matching, so the real status comes back.
	a, err := scanAttachment(pool.QueryRow(ctx,
		`SELECT `+attachmentColumns+` FROM record_email_attachment
		 WHERE record_id = $1::uuid AND provider_attachment_id = $3
		 ORDER BY created_at DESC, id DESC LIMIT 1`,
		recordID, time.Time{}, providerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrAttachmentNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get received attachment %s: %w", providerID, err)
	}
	return a, nil
}

// MarkStored records that a pending file was copied to storageKey. It reports
// false when the file was no longer pending (another delivery finished it first).
func MarkStored(ctx context.Context, pool *pgxpool.Pool, id, storageKey, contentType string, size int64) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE record_email_attachment
		SET status = $2, storage_key = $3, content_type = $4, size_bytes = $5
		WHERE id = $1::uuid AND status = $6`,
		id, AttachmentStored, storageKey, contentType, size, AttachmentPending)
	if err != nil {
		return false, fmt.Errorf("mark email attachment %s stored: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetStatus moves a pending file to a final state in which it is not kept.
func SetStatus(ctx context.Context, pool *pgxpool.Pool, id, status string) error {
	_, err := pool.Exec(ctx,
		`UPDATE record_email_attachment SET status = $2 WHERE id = $1::uuid AND status = $3`,
		id, status, AttachmentPending)
	if err != nil {
		return fmt.Errorf("set email attachment %s to %s: %w", id, status, err)
	}
	return nil
}

// StoredBytes is how much the record's conversation already keeps, for the cap.
func StoredBytes(ctx context.Context, pool *pgxpool.Pool, recordID string) (int64, error) {
	var total int64
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(size_bytes), 0)::bigint FROM record_email_attachment
		WHERE record_id = $1::uuid AND status = $2`, recordID, AttachmentStored).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum email attachment bytes for %s: %w", recordID, err)
	}
	return total, nil
}
