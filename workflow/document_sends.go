package workflow

import (
	"context"
	"fmt"
	"time"
)

// DocumentSend is one row of the document_sends history — a record of a
// generated document (PDF) that was emailed to a customer.
type DocumentSend struct {
	ID           string    `json:"id"`
	RecordID     string    `json:"recordId"`
	WorkflowKey  string    `json:"workflowKey"`
	AttachmentID string    `json:"attachmentId,omitempty"`
	SentTo       string    `json:"sentTo"`
	CC           string    `json:"cc,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	SentByUserID string    `json:"sentByUserId,omitempty"`
	SentAt       time.Time `json:"sentAt"`
}

// InsertDocumentSend records one emailed document. Returns the new row id.
func InsertDocumentSend(ctx context.Context, q Querier, s DocumentSend) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO document_sends
			(record_id, workflow_key, attachment_id, sent_to, cc, subject, sent_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id`,
		s.RecordID,
		s.WorkflowKey,
		nullIfEmpty(s.AttachmentID),
		s.SentTo,
		s.CC,
		s.Subject,
		nullIfEmpty(s.SentByUserID),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert document send: %w", err)
	}
	return id, nil
}

// ListDocumentSends returns a record's send history, newest first.
func ListDocumentSends(ctx context.Context, q Querier, recordID string) ([]DocumentSend, error) {
	rows, err := q.Query(ctx, `
		SELECT id, record_id, workflow_key,
		       COALESCE(attachment_id::text, ''), sent_to, cc, subject,
		       COALESCE(sent_by_user_id::text, ''), sent_at
		FROM document_sends
		WHERE record_id = $1
		ORDER BY sent_at DESC`, recordID)
	if err != nil {
		return nil, fmt.Errorf("query document sends: %w", err)
	}
	defer rows.Close()

	var out []DocumentSend
	for rows.Next() {
		var s DocumentSend
		if err := rows.Scan(&s.ID, &s.RecordID, &s.WorkflowKey, &s.AttachmentID,
			&s.SentTo, &s.CC, &s.Subject, &s.SentByUserID, &s.SentAt); err != nil {
			return nil, fmt.Errorf("scan document send: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
