package drawing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// coalesceIdleWindow is how long after a version was last written the same
	// person's next save is still treated as part of the same editing burst.
	// The editor autosaves 3s after every change, so without this each pause in
	// drawing would append a full-document snapshot to the history table.
	coalesceIdleWindow = 10 * time.Minute
	// coalesceMaxSpan caps one burst measured from its first save, so a
	// person who edits all afternoon still leaves restore points behind
	// instead of one ever-growing version.
	coalesceMaxSpan = time.Hour
)

// Save creates or updates the current drawing for a sales order, inside one
// transaction: lock the existing row (if any) with FOR UPDATE, compare
// expectedVersion against the locked current version (nil skips the check,
// matching write-safety patterns elsewhere in this codebase), upsert the
// live sales_order_drawing row, and record the snapshot in
// sales_order_drawing_version. Returns the saved Drawing at its new version.
//
// A save by the same person shortly after their previous one (see
// coalesceIdleWindow / coalesceMaxSpan) rewrites that previous history row
// under the new version number instead of appending another, so history holds
// one entry per editing burst rather than one per autosave. The record version
// still increments on every save, which is what keeps conflict detection
// exact; the history list simply skips the numbers that were folded together.
func Save(ctx context.Context, pool *pgxpool.Pool, salesOrderID int, d Drawing, expectedVersion *int, actorEmployeeID *int, actorName string) (*Drawing, error) {
	return save(ctx, pool, salesOrderID, d, expectedVersion, actorEmployeeID, actorName, true)
}

// canCoalesce reports whether the newest history row (the one at
// currentVersion) was written by the same person recently enough that this
// save should fold into it. An unknown actor never coalesces: there is no
// way to tell two anonymous editors apart.
func canCoalesce(ctx context.Context, tx pgx.Tx, salesOrderID, currentVersion int, actorEmployeeID *int) (bool, error) {
	if actorEmployeeID == nil {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(
			actor_employee_id = $3::int
			AND at > CURRENT_TIMESTAMP - make_interval(secs => $4::float8)
			AND COALESCE(first_at, at) > CURRENT_TIMESTAMP - make_interval(secs => $5::float8),
			FALSE)
		FROM sales_order_drawing_version
		WHERE sales_order_id = $1 AND version = $2`,
		salesOrderID, currentVersion, *actorEmployeeID,
		coalesceIdleWindow.Seconds(), coalesceMaxSpan.Seconds(),
	).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check history row: %w", err)
	}
	return ok, nil
}

// save is Save with the coalescing decision exposed: Restore passes false so an
// explicit restore always leaves its own history entry.
func save(ctx context.Context, pool *pgxpool.Pool, salesOrderID int, d Drawing, expectedVersion *int, actorEmployeeID *int, actorName string, coalesce bool) (*Drawing, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("drawing: save: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var currentVersion int
	hasRow := true
	err = tx.QueryRow(ctx, `SELECT drawing_record_version FROM sales_order_drawing WHERE sales_order_id = $1 FOR UPDATE`, salesOrderID).Scan(&currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		hasRow = false
	} else if err != nil {
		return nil, fmt.Errorf("drawing: save: lock current row: %w", err)
	}

	if expectedVersion != nil {
		expectedCurrent := 0
		if hasRow {
			expectedCurrent = currentVersion
		}
		if *expectedVersion != expectedCurrent {
			return nil, fmt.Errorf("drawing: save: %w", ErrVersionConflict)
		}
	}

	newVersion := 1
	if hasRow {
		newVersion = currentVersion + 1
	}

	areasJSON, err := json.Marshal(d.Areas)
	if err != nil {
		return nil, fmt.Errorf("drawing: save: encode areas: %w", err)
	}
	snapshotJSON, err := json.Marshal(snapshot{
		SchemaVersion: d.SchemaVersion, DisplayUnit: d.DisplayUnit,
		FractionDenominator: d.FractionDenominator, Areas: d.Areas,
	})
	if err != nil {
		return nil, fmt.Errorf("drawing: save: encode snapshot: %w", err)
	}

	var updatedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO sales_order_drawing
			(sales_order_id, schema_version, display_unit, fraction_denominator, areas, drawing_record_version, actor_employee_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_TIMESTAMP)
		ON CONFLICT (sales_order_id) DO UPDATE SET
			schema_version = EXCLUDED.schema_version,
			display_unit = EXCLUDED.display_unit,
			fraction_denominator = EXCLUDED.fraction_denominator,
			areas = EXCLUDED.areas,
			drawing_record_version = EXCLUDED.drawing_record_version,
			actor_employee_id = EXCLUDED.actor_employee_id,
			updated_at = EXCLUDED.updated_at
		RETURNING updated_at`,
		salesOrderID, d.SchemaVersion, d.DisplayUnit, d.FractionDenominator, areasJSON, newVersion, actorEmployeeID,
	).Scan(&updatedAt)
	if err != nil {
		return nil, fmt.Errorf("drawing: save: upsert current: %w", err)
	}

	folded := false
	if coalesce && hasRow {
		folded, err = canCoalesce(ctx, tx, salesOrderID, currentVersion, actorEmployeeID)
		if err != nil {
			return nil, fmt.Errorf("drawing: save: %w", err)
		}
	}
	if folded {
		// first_at keeps the burst's start across repeated folds so
		// coalesceMaxSpan measures from the first save, not the latest.
		if _, err := tx.Exec(ctx, `
			UPDATE sales_order_drawing_version
			SET version = $3, snapshot = $4, at = CURRENT_TIMESTAMP, first_at = COALESCE(first_at, at)
			WHERE sales_order_id = $1 AND version = $2`,
			salesOrderID, currentVersion, newVersion, snapshotJSON,
		); err != nil {
			return nil, fmt.Errorf("drawing: save: fold into version history: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `
		INSERT INTO sales_order_drawing_version (sales_order_id, version, snapshot, actor_employee_id, at)
		VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP)`,
		salesOrderID, newVersion, snapshotJSON, actorEmployeeID,
	); err != nil {
		return nil, fmt.Errorf("drawing: save: write version history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("drawing: save: commit: %w", err)
	}

	actorID := actorEmployeeID
	name := actorName
	return &Drawing{
		SchemaVersion: d.SchemaVersion, Version: newVersion, DisplayUnit: d.DisplayUnit,
		FractionDenominator: d.FractionDenominator, Areas: d.Areas,
		UpdatedAt: isoTime(updatedAt),
		UpdatedBy: updatedByFromActor(actorID, name),
	}, nil
}

// updatedByFromActor builds an UpdatedBy from the caller's own already-known
// employee id/name (Save's caller knows who it is acting as; Get/GetVersion
// resolve the name from a stored id instead, via updatedByFrom in store.go).
func updatedByFromActor(actorEmployeeID *int, name string) UpdatedBy {
	if actorEmployeeID == nil {
		return UpdatedBy{Name: name}
	}
	return UpdatedBy{ID: strconv.Itoa(*actorEmployeeID), Name: name}
}
