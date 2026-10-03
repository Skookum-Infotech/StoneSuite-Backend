package docextractjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// MarkQueued moves the caller's awaiting_upload extraction to queued, recording
// the client's claimed SHA-256 and the enqueued job id. It is idempotent: when
// the extraction is already past awaiting_upload it changes nothing and
// returns changed=false with the current status.
func (s *Store) MarkQueued(ctx context.Context, id, ownerIdentityID, clientSHA256, jobID string) (status string, changed bool, err error) {
	if !validUUID(id) {
		return "", false, ErrNotFound
	}
	err = s.pool.QueryRow(ctx, `
		UPDATE document_extractions
		SET status = $3, client_sha256 = $4, job_id = $5, updated_at = NOW()
		WHERE id = $1::uuid AND owner_identity_id = $2 AND status = $6 AND expires_at > NOW()
		RETURNING status`,
		id, ownerIdentityID, StatusQueued, strings.ToLower(clientSHA256), jobID, StatusAwaitingUpload).Scan(&status)
	if err == nil {
		return status, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("mark extraction queued: %w", err)
	}
	err = s.pool.QueryRow(ctx,
		`SELECT status FROM document_extractions WHERE id = $1::uuid AND owner_identity_id = $2`,
		id, ownerIdentityID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("read extraction status: %w", err)
	}
	return status, false, nil
}

// MarkRunning marks a queued (or already running, after a requeue) extraction
// running. ErrConflict means it was discarded or finished meanwhile.
func (s *Store) MarkRunning(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE document_extractions SET status = $2, updated_at = NOW()
		WHERE id = $1::uuid AND status = ANY($3)`,
		id, StatusRunning, []string{StatusQueued, StatusRunning})
	if err != nil {
		return fmt.Errorf("mark extraction running: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// SaveResultParams is what the worker persists on success.
type SaveResultParams struct {
	SHA256 string
	Method string
	Model  string
	Result ResultDoc
}

// SaveResult stores the resolved result and moves a running extraction to
// ready. ErrConflict means it was discarded meanwhile.
func (s *Store) SaveResult(ctx context.Context, id string, p SaveResultParams) error {
	body, err := json.Marshal(p.Result)
	if err != nil {
		return fmt.Errorf("marshal extraction result: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE document_extractions
		SET status = $2, sha256 = $3, method = $4, model = $5, result = $6, failure_code = '', updated_at = NOW()
		WHERE id = $1::uuid AND status = $7`,
		id, StatusReady, p.SHA256, p.Method, p.Model, body, StatusRunning)
	if err != nil {
		return fmt.Errorf("save extraction result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// ReplaceReadyResult swaps in a re-resolved result while the extraction is
// still ready. ErrConflict means it was used or discarded meanwhile.
func (s *Store) ReplaceReadyResult(ctx context.Context, id string, res ResultDoc) error {
	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("marshal extraction result: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE document_extractions SET result = $2, updated_at = NOW()
		WHERE id = $1::uuid AND status = $3`, id, body, StatusReady)
	if err != nil {
		return fmt.Errorf("replace extraction result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// MarkFailed moves a queued or running extraction to failed with a stable code.
func (s *Store) MarkFailed(ctx context.Context, id, failureCode string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE document_extractions SET status = $2, failure_code = $3, updated_at = NOW()
		WHERE id = $1::uuid AND status = ANY($4)`,
		id, StatusFailed, failureCode, []string{StatusQueued, StatusRunning}); err != nil {
		return fmt.Errorf("mark extraction failed: %w", err)
	}
	return nil
}

// SetNotify sets notify_on_complete on the caller's own live extraction.
func (s *Store) SetNotify(ctx context.Context, id, ownerIdentityID string, notify bool) error {
	if !validUUID(id) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE document_extractions SET notify_on_complete = $3, updated_at = NOW()
		WHERE id = $1::uuid AND owner_identity_id = $2 AND status <> ALL($4)`,
		id, ownerIdentityID, notify, []string{StatusDiscarded, StatusUsed, StatusAttached})
	if err != nil {
		return fmt.Errorf("set extraction notify: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.notFoundOrConflict(ctx, id, ownerIdentityID)
	}
	return nil
}

// CompleteCAS atomically moves the caller's ready, non-expired extraction to
// used and records the created record's uuid. A second call (or a different
// state) returns ErrConflict; another owner's id returns ErrNotFound.
func (s *Store) CompleteCAS(ctx context.Context, id, ownerIdentityID, recordUUID string) error {
	if !validUUID(id) || !validUUID(recordUUID) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE document_extractions
		SET status = $3, record_uuid = $4::uuid, updated_at = NOW()
		WHERE id = $1::uuid AND owner_identity_id = $2 AND status = $5 AND expires_at > NOW()`,
		id, ownerIdentityID, StatusUsed, recordUUID, StatusReady)
	if err != nil {
		return fmt.Errorf("complete extraction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.notFoundOrConflict(ctx, id, ownerIdentityID)
	}
	return nil
}
