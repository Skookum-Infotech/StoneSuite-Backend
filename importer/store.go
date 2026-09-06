package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Row is one staged candidate record — a row from import_rows.
type Row struct {
	ID       string
	JobID    string
	RowIndex int
	// Raw is the parsed row/extracted fields exactly as found, before any
	// mapping — display-only (e.g. "here's what we found in column 3"),
	// never read back programmatically, so it stays a raw JSON value rather
	// than a typed Go value.
	Raw json.RawMessage
	// Mapped is Raw's fields resolved onto the target record's shape —
	// what Commit actually reads.
	Mapped   MappedFields
	Errors   []string
	Status   string
	RecordID string // set once committed
}

// Store persists import_rows for one tenant.
type Store struct{ pool *pgxpool.Pool }

// NewStore builds a Store over a tenant pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// InsertRow stages one candidate record. errs is a hint from partial
// validation at staging time (see stageErrors) — informational only, shown
// to a reviewer before commit; it does not block InsertRow or later commit
// attempts, which re-validate fully.
func (s *Store) InsertRow(ctx context.Context, jobID string, rowIndex int, raw any, mapped MappedFields, errs []string) (string, error) {
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("marshal raw row: %w", err)
	}
	mappedJSON, err := json.Marshal(mapped)
	if err != nil {
		return "", fmt.Errorf("marshal mapped fields: %w", err)
	}
	errsJSON, err := marshalErrors(errs)
	if err != nil {
		return "", err
	}

	var id string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO import_rows (job_id, row_index, raw, mapped, errors, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		jobID, rowIndex, rawJSON, mappedJSON, errsJSON, StatusPending,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert import row: %w", err)
	}
	return id, nil
}

// ListByJob returns every row of jobID, in row_index order.
func (s *Store) ListByJob(ctx context.Context, jobID string) ([]Row, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, job_id, row_index, raw, mapped, errors, status, COALESCE(record_id::text, '')
		FROM import_rows WHERE job_id = $1 ORDER BY row_index`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list import rows: %w", err)
	}
	defer rows.Close()

	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRow loads one row by id. found is false if it doesn't exist.
func (s *Store) GetRow(ctx context.Context, id string) (row Row, found bool, err error) {
	r := s.pool.QueryRow(ctx, `
		SELECT id, job_id, row_index, raw, mapped, errors, status, COALESCE(record_id::text, '')
		FROM import_rows WHERE id = $1`, id)
	row, err = scanRow(r)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return Row{}, false, nil
		}
		return Row{}, false, err
	}
	return row, true, nil
}

// UpdateMapped overwrites a row's mapped fields (the review/correct step)
// and resets its status to pending — a row a reviewer just edited should be
// reconsidered on the next commit attempt, even if it was previously
// skipped or failed.
func (s *Store) UpdateMapped(ctx context.Context, id string, mapped MappedFields) error {
	body, err := json.Marshal(mapped)
	if err != nil {
		return fmt.Errorf("marshal mapped fields: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE import_rows SET mapped = $2, status = $3, errors = '[]'::jsonb, updated_at = NOW()
		WHERE id = $1 AND status <> $4`,
		id, body, StatusPending, StatusCommitted); err != nil {
		return fmt.Errorf("update import row: %w", err)
	}
	return nil
}

// MarkSkipped excludes a row from commit without deleting it — the
// reviewer's call that this candidate simply isn't a record. A no-op on an
// already-committed row (its outcome is final).
func (s *Store) MarkSkipped(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE import_rows SET status = $2, updated_at = NOW() WHERE id = $1 AND status <> $3`,
		id, StatusSkipped, StatusCommitted); err != nil {
		return fmt.Errorf("mark import row skipped: %w", err)
	}
	return nil
}

// MarkCommitted records the CRM record a row produced. recordID doubles as
// this row's commit idempotency key — see CommitJob.
func (s *Store) MarkCommitted(ctx context.Context, id, recordID string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE import_rows SET status = $2, record_id = $3, errors = '[]'::jsonb, updated_at = NOW()
		WHERE id = $1`,
		id, StatusCommitted, recordID); err != nil {
		return fmt.Errorf("mark import row committed: %w", err)
	}
	return nil
}

// MarkFailed records why a commit attempt failed, without changing the
// row's status to a terminal state — a failed row is still StatusFailed,
// which a reviewer can correct (UpdateMapped resets it to pending) and
// resubmit.
func (s *Store) MarkFailed(ctx context.Context, id string, errs []string) error {
	body, err := marshalErrors(errs)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE import_rows SET status = $2, errors = $3, updated_at = NOW() WHERE id = $1`,
		id, StatusFailed, body); err != nil {
		return fmt.Errorf("mark import row failed: %w", err)
	}
	return nil
}

func marshalErrors(errs []string) ([]byte, error) {
	if errs == nil {
		errs = []string{}
	}
	body, err := json.Marshal(errs)
	if err != nil {
		return nil, fmt.Errorf("marshal row errors: %w", err)
	}
	return body, nil
}

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// so scanRow works for both GetRow and ListByJob.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(sc rowScanner) (Row, error) {
	var r Row
	var mappedJSON, errsJSON []byte
	if err := sc.Scan(&r.ID, &r.JobID, &r.RowIndex, &r.Raw, &mappedJSON, &errsJSON, &r.Status, &r.RecordID); err != nil {
		return Row{}, fmt.Errorf("scan import row: %w", err)
	}
	if err := json.Unmarshal(mappedJSON, &r.Mapped); err != nil {
		return Row{}, fmt.Errorf("unmarshal mapped fields: %w", err)
	}
	if err := json.Unmarshal(errsJSON, &r.Errors); err != nil {
		return Row{}, fmt.Errorf("unmarshal row errors: %w", err)
	}
	return r, nil
}
