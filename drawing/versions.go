package drawing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VersionSummary is one row of a drawing's version history, without its
// full snapshot payload.
type VersionSummary struct {
	Version   int       `json:"version"`
	UpdatedAt string    `json:"updatedAt"`
	UpdatedBy UpdatedBy `json:"updatedBy"`
}

func encodeVersionCursor(version int) string {
	return base64.URLEncoding.EncodeToString([]byte(strconv.Itoa(version)))
}

func decodeVersionCursor(cursor string) (int, error) {
	b, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("drawing: invalid cursor: %w", err)
	}
	v, err := strconv.Atoi(string(b))
	if err != nil {
		return 0, fmt.Errorf("drawing: invalid cursor: %w", err)
	}
	return v, nil
}

// ListVersions returns a page of version summaries, newest first, keyset
// paginated on version (never an offset, per this project's pagination
// convention). An empty cursor starts from the newest version.
func ListVersions(ctx context.Context, pool *pgxpool.Pool, salesOrderID int, cursor string, limit int) ([]VersionSummary, string, error) {
	args := []any{salesOrderID}
	query := `
		SELECT v.version, v.at, v.actor_employee_id, e.employee_first_name, e.employee_last_name
		FROM sales_order_drawing_version v
		LEFT JOIN employee e ON e.employee_id = v.actor_employee_id
		WHERE v.sales_order_id = $1`
	if cursor != "" {
		before, err := decodeVersionCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		query += " AND v.version < $2"
		args = append(args, before)
	}
	query += " ORDER BY v.version DESC LIMIT " + strconv.Itoa(limit+1)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("drawing: list versions: %w", err)
	}
	defer rows.Close()

	var items []VersionSummary
	for rows.Next() {
		var (
			v           int
			at          time.Time
			actorID     *int
			first, last *string
		)
		if err := rows.Scan(&v, &at, &actorID, &first, &last); err != nil {
			return nil, "", fmt.Errorf("drawing: list versions: scan: %w", err)
		}
		items = append(items, VersionSummary{Version: v, UpdatedAt: isoTime(at), UpdatedBy: updatedByFrom(actorID, first, last)})
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("drawing: list versions: %w", err)
	}

	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		nextCursor = encodeVersionCursor(items[len(items)-1].Version)
	}
	return items, nextCursor, nil
}

// GetVersion returns the Drawing exactly as it was at the given version, or
// an ErrNotFound-wrapped error if that version doesn't exist for this sales
// order.
func GetVersion(ctx context.Context, pool *pgxpool.Pool, salesOrderID, version int) (*Drawing, error) {
	var (
		snapshotRaw []byte
		actorID     *int
		first, last *string
		at          time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT v.snapshot, v.at, v.actor_employee_id, e.employee_first_name, e.employee_last_name
		FROM sales_order_drawing_version v
		LEFT JOIN employee e ON e.employee_id = v.actor_employee_id
		WHERE v.sales_order_id = $1 AND v.version = $2`, salesOrderID, version,
	).Scan(&snapshotRaw, &at, &actorID, &first, &last)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("drawing: get version: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("drawing: get version: %w", err)
	}
	var s snapshot
	if err := json.Unmarshal(snapshotRaw, &s); err != nil {
		return nil, fmt.Errorf("drawing: get version: decode snapshot: %w", err)
	}
	return &Drawing{
		SchemaVersion: s.SchemaVersion, Version: version, DisplayUnit: s.DisplayUnit,
		FractionDenominator: s.FractionDenominator, Areas: s.Areas,
		UpdatedAt: isoTime(at), UpdatedBy: updatedByFrom(actorID, first, last),
	}, nil
}

// Restore loads the content of an old version and saves it as a brand new
// version -- restoring never rewinds history, per the spec.
func Restore(ctx context.Context, pool *pgxpool.Pool, salesOrderID, version int, expectedVersion *int, actorEmployeeID *int, actorName string) (*Drawing, error) {
	old, err := GetVersion(ctx, pool, salesOrderID, version)
	if err != nil {
		return nil, err
	}
	return Save(ctx, pool, salesOrderID, *old, expectedVersion, actorEmployeeID, actorName)
}
