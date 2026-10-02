package docextractjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors returned by the Store.
var (
	// ErrNotFound means no extraction with that id exists for that owner.
	ErrNotFound = errors.New("docextractjob: extraction not found")
	// ErrExpired means the extraction outlived its expires_at and was deleted.
	ErrExpired = errors.New("docextractjob: extraction expired")
	// ErrConflict means the extraction exists but is not in a state that allows the change.
	ErrConflict = errors.New("docextractjob: extraction state does not allow this")
	// ErrBadExtension means the staging file extension is not allowed.
	ErrBadExtension = errors.New("docextractjob: unsupported file extension")
)

// stagingKeyFormat is {tenantSlug}/_staging/doc-extract/{id}.{ext}.
const stagingKeyFormat = "%s/_staging/doc-extract/%s.%s"

// allowedExtensions are the staging file extensions Create accepts.
var allowedExtensions = map[string]struct{}{"pdf": {}, "docx": {}}

// maxReadyList caps ListReadyForOwner.
const maxReadyList = 50

// Extraction is one document_extractions row.
type Extraction struct {
	ID               string          `json:"id"`
	DocType          string          `json:"docType"`
	OwnerIdentityID  string          `json:"-"`
	Status           string          `json:"status"`
	ClientSHA256     string          `json:"-"`
	FileName         string          `json:"fileName"`
	ContentType      string          `json:"contentType"`
	SizeBytes        int64           `json:"sizeBytes"`
	SHA256           string          `json:"-"`
	StagingKey       string          `json:"-"`
	JobID            string          `json:"-"`
	Method           string          `json:"method"`
	Model            string          `json:"model,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	FailureCode      string          `json:"failureCode,omitempty"`
	NotifyOnComplete bool            `json:"notifyOnComplete"`
	RecordUUID       string          `json:"recordUuid,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	ExpiresAt        time.Time       `json:"expiresAt"`
}

// Store persists document_extractions for one tenant (a tenant pool).
type Store struct{ pool *pgxpool.Pool }

// NewStore builds a Store over a tenant pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// CreateParams are the server-trusted inputs of Create.
type CreateParams struct {
	DocType         string
	OwnerIdentityID string
	FileName        string
	ContentType     string
	SizeBytes       int64
	TenantSlug      string
	Extension       string // "pdf" or "docx", already validated by the caller against the magic bytes it can see
	TTL             time.Duration
}

// extractionColumns is the shared SELECT list; the order matches scanExtraction.
const extractionColumns = `id::text, doc_type, owner_identity_id, status, client_sha256, file_name, content_type,
	size_bytes, sha256, staging_key, job_id, method, model, result, failure_code, notify_on_complete,
	COALESCE(record_uuid::text, ''), created_at, updated_at, expires_at`

// scanExtraction scans one extractionColumns row.
func scanExtraction(row pgx.Row) (*Extraction, error) {
	var e Extraction
	if err := row.Scan(&e.ID, &e.DocType, &e.OwnerIdentityID, &e.Status, &e.ClientSHA256, &e.FileName, &e.ContentType,
		&e.SizeBytes, &e.SHA256, &e.StagingKey, &e.JobID, &e.Method, &e.Model, &e.Result, &e.FailureCode, &e.NotifyOnComplete,
		&e.RecordUUID, &e.CreatedAt, &e.UpdatedAt, &e.ExpiresAt); err != nil {
		return nil, err
	}
	return &e, nil
}

// StagingKey builds the server-generated staging object key for an extraction id.
func StagingKey(tenantSlug, id, ext string) string {
	return fmt.Sprintf(stagingKeyFormat, tenantSlug, id, ext)
}

// newID returns a random RFC 4122 version-4 UUID string.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate extraction id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// Create inserts an awaiting_upload extraction whose staging key is generated
// here from the new id -- a client-supplied key is never accepted.
func (s *Store) Create(ctx context.Context, p CreateParams) (*Extraction, error) {
	ext := strings.ToLower(strings.TrimPrefix(p.Extension, "."))
	if _, ok := allowedExtensions[ext]; !ok {
		return nil, ErrBadExtension
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO document_extractions
			(id, doc_type, owner_identity_id, status, file_name, content_type, size_bytes, staging_key, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW() + make_interval(secs => $9))
		RETURNING `+extractionColumns,
		id, p.DocType, p.OwnerIdentityID, StatusAwaitingUpload, p.FileName, p.ContentType, p.SizeBytes,
		StagingKey(p.TenantSlug, id, ext), p.TTL.Seconds())
	e, err := scanExtraction(row)
	if err != nil {
		return nil, fmt.Errorf("create extraction: %w", err)
	}
	return e, nil
}

// Get returns the caller's own extraction. Another owner's id is
// ErrNotFound -- the owner check is inside the SQL, so existence never leaks.
// A row past expires_at (and not attached) is deleted on the spot: Get returns
// the row (so the caller can delete its staging object) together with
// ErrExpired.
func (s *Store) Get(ctx context.Context, id, ownerIdentityID string) (*Extraction, error) {
	if !validUUID(id) {
		return nil, ErrNotFound
	}
	var expired bool
	row := s.pool.QueryRow(ctx, `
		SELECT `+extractionColumns+`, expires_at < NOW()
		FROM document_extractions WHERE id = $1::uuid AND owner_identity_id = $2`, id, ownerIdentityID)
	e, err := scanExtractionWithExpiry(row, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get extraction: %w", err)
	}
	if expired && e.Status != StatusAttached {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM document_extractions WHERE id = $1::uuid AND owner_identity_id = $2 AND status <> $3`,
			id, ownerIdentityID, StatusAttached); err != nil {
			return nil, fmt.Errorf("delete expired extraction: %w", err)
		}
		return e, ErrExpired
	}
	return e, nil
}

// scanExtractionWithExpiry scans extractionColumns plus a trailing expired flag.
func scanExtractionWithExpiry(row pgx.Row, expired *bool) (*Extraction, error) {
	var e Extraction
	if err := row.Scan(&e.ID, &e.DocType, &e.OwnerIdentityID, &e.Status, &e.ClientSHA256, &e.FileName, &e.ContentType,
		&e.SizeBytes, &e.SHA256, &e.StagingKey, &e.JobID, &e.Method, &e.Model, &e.Result, &e.FailureCode, &e.NotifyOnComplete,
		&e.RecordUUID, &e.CreatedAt, &e.UpdatedAt, &e.ExpiresAt, expired); err != nil {
		return nil, err
	}
	return &e, nil
}

// GetInternal loads an extraction by id with no owner check, for the worker
// only (it runs with no caller). Controllers must use Get.
func (s *Store) GetInternal(ctx context.Context, id string) (*Extraction, error) {
	if !validUUID(id) {
		return nil, ErrNotFound
	}
	e, err := scanExtraction(s.pool.QueryRow(ctx,
		`SELECT `+extractionColumns+` FROM document_extractions WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get extraction %s: %w", id, err)
	}
	return e, nil
}

// ListReadyForOwner returns the caller's own non-expired ready extractions,
// newest first, without the result body (the "Pending documents" chip).
func (s *Store) ListReadyForOwner(ctx context.Context, ownerIdentityID string) ([]Extraction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+extractionColumns+`
		FROM document_extractions
		WHERE owner_identity_id = $1 AND status = $2 AND expires_at > NOW()
		ORDER BY created_at DESC LIMIT $3`, ownerIdentityID, StatusReady, maxReadyList)
	if err != nil {
		return nil, fmt.Errorf("list ready extractions: %w", err)
	}
	defer rows.Close()
	out := []Extraction{}
	for rows.Next() {
		e, err := scanExtraction(rows)
		if err != nil {
			return nil, fmt.Errorf("scan extraction: %w", err)
		}
		e.Result = nil
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate extractions: %w", err)
	}
	return out, nil
}

// Discard marks the caller's extraction discarded and returns its staging key
// so the caller can delete the object. It is idempotent; a used or attached
// extraction is ErrConflict.
func (s *Store) Discard(ctx context.Context, id, ownerIdentityID string) (string, error) {
	if !validUUID(id) {
		return "", ErrNotFound
	}
	var key string
	err := s.pool.QueryRow(ctx, `
		UPDATE document_extractions SET status = $3, updated_at = NOW()
		WHERE id = $1::uuid AND owner_identity_id = $2 AND status <> ALL($4)
		RETURNING staging_key`,
		id, ownerIdentityID, StatusDiscarded, []string{StatusUsed, StatusAttached}).Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("discard extraction: %w", err)
	}
	return "", s.notFoundOrConflict(ctx, id, ownerIdentityID)
}

// notFoundOrConflict distinguishes "no such extraction for this owner" from
// "exists but wrong state" after a guarded UPDATE touched no row.
func (s *Store) notFoundOrConflict(ctx context.Context, id, ownerIdentityID string) error {
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM document_extractions WHERE id = $1::uuid AND owner_identity_id = $2`, id, ownerIdentityID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check extraction: %w", err)
	}
	return ErrConflict
}
