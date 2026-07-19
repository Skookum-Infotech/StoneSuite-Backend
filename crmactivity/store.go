package crmactivity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when an activity (or its parent CRM record) uuid
// matches nothing live.
var ErrNotFound = errors.New("activity not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring vendors.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// resolveCustomerInternalID resolves a CRM record's external uuid to the
// internal customer_id backing it. Only the v2 relational CRM design
// (lead/prospect/customer all live in the `customer` table) is supported —
// a v1 (legacy JSONB workflow_records) tenant has no matching row here and
// gets ErrNotFound, same as an unknown uuid.
func resolveCustomerInternalID(ctx context.Context, pool *pgxpool.Pool, recordUUID string) (int, error) {
	var id int
	err := pool.QueryRow(ctx,
		`SELECT customer_id FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, recordUUID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve customer for activity: %w", err)
	}
	return id, nil
}

const activitySelect = `
	SELECT a.crm_activity_uuid, c.customer_uuid, a.activity_type, a.occurred_at,
	       COALESCE(ou.id::text,''), COALESCE(TRIM(oe.employee_first_name || ' ' || oe.employee_last_name),''),
	       a.subject, a.body, a.created_at, a.updated_at
	FROM crm_activity a
	JOIN customer c ON c.customer_id = a.customer_id
	LEFT JOIN employee oe ON oe.employee_id = a.author_employee_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanActivity(row pgx.Row) (*Activity, error) {
	var a Activity
	if err := row.Scan(
		&a.ID, &a.RecordID, &a.ActivityType, &a.OccurredAt,
		&a.Author.ID, &a.Author.Name,
		&a.Subject, &a.Body, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &a, nil
}

// Get loads a single live activity by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, activityUUID string) (*Activity, error) {
	a, err := scanActivity(pool.QueryRow(ctx, activitySelect+`
		WHERE a.crm_activity_uuid = $1 AND a.deleted_at IS NULL`, activityUUID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get activity: %w", err)
	}
	return a, nil
}

// validateFields checks the shared create/update payload shape.
func validateFields(f activityFields) (time.Time, error) {
	t := f.ActivityType
	if !ValidTypes[t] {
		return time.Time{}, ClientError{Msg: "activityType must be one of call, email, meeting, note, task."}
	}
	if f.OccurredAt == "" {
		return time.Now().UTC(), nil
	}
	occurred, err := time.Parse(time.RFC3339, f.OccurredAt)
	if err != nil {
		return time.Time{}, ClientError{Msg: "occurredAt must be an RFC3339 timestamp."}
	}
	return occurred, nil
}

// Create inserts a new activity against the CRM record identified by
// recordUUID (a lead/prospect/customer, all backed by the `customer` table).
func Create(ctx context.Context, pool *pgxpool.Pool, recordUUID string, in CreateActivityInput, actorEmployeeID int) (*Activity, error) {
	custInternalID, err := resolveCustomerInternalID(ctx, pool, recordUUID)
	if err != nil {
		return nil, err
	}
	occurred, err := validateFields(in.activityFields)
	if err != nil {
		return nil, err
	}

	var newUUID string
	err = pool.QueryRow(ctx, `
		INSERT INTO crm_activity (customer_id, activity_type, occurred_at, author_employee_id, subject, body, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $4)
		RETURNING crm_activity_uuid`,
		custInternalID, in.ActivityType, occurred, nullableInt(actorEmployeeID),
		strings.TrimSpace(in.Subject), in.Body,
	).Scan(&newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert activity: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// List returns a CRM record's live activities, most recent first, optionally
// filtered to one activity type. Bounded child collection under one parent
// record — no keyset pagination (mirrors the audit-trail endpoints).
func List(ctx context.Context, pool *pgxpool.Pool, recordUUID, activityType string) ([]Activity, error) {
	custInternalID, err := resolveCustomerInternalID(ctx, pool, recordUUID)
	if err != nil {
		return nil, err
	}
	q := activitySelect + ` WHERE a.customer_id = $1 AND a.deleted_at IS NULL`
	args := []any{custInternalID}
	if activityType != "" {
		if !ValidTypes[activityType] {
			return nil, ClientError{Msg: "activityType must be one of call, email, meeting, note, task."}
		}
		q += ` AND a.activity_type = $2`
		args = append(args, activityType)
	}
	q += ` ORDER BY a.occurred_at DESC, a.crm_activity_id DESC`

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list activities: %w", err)
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, fmt.Errorf("scan activity: %w", err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// verifyBelongsToRecord confirms activityUUID is a live activity attached to
// recordUUID's customer — defense in depth against a caller who holds scope
// on one CRM record supplying a different, unrelated activityId in the path.
func verifyBelongsToRecord(ctx context.Context, pool *pgxpool.Pool, recordUUID, activityUUID string) (int, error) {
	var activityInternalID, customerUUIDMatch int
	err := pool.QueryRow(ctx, `
		SELECT a.crm_activity_id, 1
		FROM crm_activity a
		JOIN customer c ON c.customer_id = a.customer_id
		WHERE a.crm_activity_uuid = $1 AND c.customer_uuid = $2 AND a.deleted_at IS NULL`,
		activityUUID, recordUUID).Scan(&activityInternalID, &customerUUIDMatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("verify activity ownership: %w", err)
	}
	return activityInternalID, nil
}

// Update replaces a live activity's editable fields.
func Update(ctx context.Context, pool *pgxpool.Pool, recordUUID, activityUUID string, in UpdateActivityInput, actorEmployeeID int) (*Activity, error) {
	internalID, err := verifyBelongsToRecord(ctx, pool, recordUUID, activityUUID)
	if err != nil {
		return nil, err
	}
	occurred, err := validateFields(in.activityFields)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
		UPDATE crm_activity SET
			activity_type = $1, occurred_at = $2, subject = $3, body = $4,
			updated_at = CURRENT_TIMESTAMP, updated_by = $5, record_version = record_version + 1
		WHERE crm_activity_id = $6`,
		in.ActivityType, occurred, strings.TrimSpace(in.Subject), in.Body,
		nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return nil, fmt.Errorf("update activity: %w", err)
	}
	return Get(ctx, pool, activityUUID)
}

// SoftDelete marks a live activity deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, recordUUID, activityUUID string, actorEmployeeID int) error {
	internalID, err := verifyBelongsToRecord(ctx, pool, recordUUID, activityUUID)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		UPDATE crm_activity SET deleted_at = CURRENT_TIMESTAMP, deleted_by = $1
		WHERE crm_activity_id = $2`,
		nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return fmt.Errorf("delete activity: %w", err)
	}
	return nil
}
