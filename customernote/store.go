package customernote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a note (or its parent CRM record) uuid
// matches nothing live.
var ErrNotFound = errors.New("customer note not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring crmactivity.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

const noteSelect = `
	SELECT n.customer_note_uuid, c.customer_uuid, n.body, n.status,
	       ci.id::text, COALESCE(ci.full_name,''), ci.email,
	       n.created_at, n.updated_at
	FROM customer_note n
	JOIN customer c ON c.customer_id = n.customer_id
	JOIN customer_identities ci ON ci.id = n.customer_identity_id`

func scanNote(row pgx.Row) (*CustomerNote, error) {
	var n CustomerNote
	if err := row.Scan(
		&n.ID, &n.RecordID, &n.Body, &n.Status,
		&n.Submitter.ID, &n.Submitter.Name, &n.Submitter.Email,
		&n.CreatedAt, &n.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &n, nil
}

// systemEmployeeID is the fallback actor for soft-delete columns that must
// never be NULL when their paired deleted_at timestamp is set (enforced by
// a CHECK constraint) — used when the caller has no resolvable employee id.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID if it's unset
// (0). Use this — never nullableInt — for deleted_by_employee_id.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

// Get loads a single live note by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, noteUUID string) (*CustomerNote, error) {
	n, err := scanNote(pool.QueryRow(ctx, noteSelect+` WHERE n.customer_note_uuid = $1 AND n.deleted_at IS NULL`, noteUUID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get customer note: %w", err)
	}
	return n, nil
}

func validateBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ClientError{Msg: "body must not be empty."}
	}
	return body, nil
}

// Create inserts a new note authored by the given customer identity, against
// its own linked CRM customer record. customerID and customerIdentityID must
// come from a verified customer JWT — never from request input.
func Create(ctx context.Context, pool *pgxpool.Pool, customerID int, customerIdentityID string, in CreateNoteInput) (*CustomerNote, error) {
	body, err := validateBody(in.Body)
	if err != nil {
		return nil, err
	}

	var newUUID string
	err = pool.QueryRow(ctx, `
		INSERT INTO customer_note (customer_id, customer_identity_id, body)
		VALUES ($1, $2, $3)
		RETURNING customer_note_uuid`,
		customerID, customerIdentityID, body,
	).Scan(&newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert customer note: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// ListByCustomerID returns a customer's own notes, most recent first. The
// caller must have already resolved customerID from a verified JWT — this is
// the only scope filter, so it must never take an id from request input.
func ListByCustomerID(ctx context.Context, pool *pgxpool.Pool, customerID int) ([]CustomerNote, error) {
	rows, err := pool.Query(ctx, noteSelect+`
		WHERE n.customer_id = $1 AND n.deleted_at IS NULL
		ORDER BY n.created_at DESC, n.customer_note_id DESC`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list own customer notes: %w", err)
	}
	defer rows.Close()
	return scanNotes(rows)
}

// resolveCustomerInternalID resolves a CRM record's external uuid to the
// internal customer_id backing it, mirroring crmactivity's helper of the
// same name.
func resolveCustomerInternalID(ctx context.Context, pool *pgxpool.Pool, recordUUID string) (int, error) {
	var id int
	err := pool.QueryRow(ctx,
		`SELECT customer_id FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, recordUUID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve customer for note: %w", err)
	}
	return id, nil
}

// ListByCustomerRecord returns a CRM customer record's notes, most recent
// first — the staff-facing view, scoped by the parent record's external uuid.
func ListByCustomerRecord(ctx context.Context, pool *pgxpool.Pool, recordUUID string) ([]CustomerNote, error) {
	custInternalID, err := resolveCustomerInternalID(ctx, pool, recordUUID)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, noteSelect+`
		WHERE n.customer_id = $1 AND n.deleted_at IS NULL
		ORDER BY n.created_at DESC, n.customer_note_id DESC`, custInternalID)
	if err != nil {
		return nil, fmt.Errorf("list customer notes: %w", err)
	}
	defer rows.Close()
	return scanNotes(rows)
}

func scanNotes(rows pgx.Rows) ([]CustomerNote, error) {
	out := []CustomerNote{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan customer note: %w", err)
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// verifyBelongsToRecord confirms noteUUID is a note attached to recordUUID's
// customer — defense in depth against a caller who holds scope on one CRM
// record supplying a different, unrelated noteId in the path.
func verifyBelongsToRecord(ctx context.Context, pool *pgxpool.Pool, recordUUID, noteUUID string) (int, error) {
	var internalID int
	err := pool.QueryRow(ctx, `
		SELECT n.customer_note_id
		FROM customer_note n
		JOIN customer c ON c.customer_id = n.customer_id
		WHERE n.customer_note_uuid = $1 AND c.customer_uuid = $2 AND n.deleted_at IS NULL`,
		noteUUID, recordUUID).Scan(&internalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("verify note ownership: %w", err)
	}
	return internalID, nil
}

// UpdateStatus sets a note's triage status, recording which staff member made
// the change.
func UpdateStatus(ctx context.Context, pool *pgxpool.Pool, recordUUID, noteUUID string, in UpdateStatusInput, actorEmployeeID int) (*CustomerNote, error) {
	internalID, err := verifyBelongsToRecord(ctx, pool, recordUUID, noteUUID)
	if err != nil {
		return nil, err
	}
	if !ValidStatuses[in.Status] {
		return nil, ClientError{Msg: "status must be one of new, read, resolved."}
	}
	_, err = pool.Exec(ctx, `
		UPDATE customer_note SET
			status = $1, updated_at = CURRENT_TIMESTAMP, updated_by_employee_id = $2
		WHERE customer_note_id = $3`,
		in.Status, nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return nil, fmt.Errorf("update customer note status: %w", err)
	}
	return Get(ctx, pool, noteUUID)
}

// SoftDelete marks a live note deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, recordUUID, noteUUID string, actorEmployeeID int) error {
	internalID, err := verifyBelongsToRecord(ctx, pool, recordUUID, noteUUID)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		UPDATE customer_note SET deleted_at = CURRENT_TIMESTAMP, deleted_by_employee_id = $1
		WHERE customer_note_id = $2`,
		actorOrSystem(actorEmployeeID), internalID)
	if err != nil {
		return fmt.Errorf("delete customer note: %w", err)
	}
	return nil
}

// nullableInt converts a non-positive id to SQL NULL, mirroring
// crmactivity's helper of the same name.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
