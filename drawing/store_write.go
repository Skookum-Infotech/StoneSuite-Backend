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

// Save creates or updates the current drawing for a sales order, inside one
// transaction: lock the existing row (if any) with FOR UPDATE, compare
// expectedVersion against the locked current version (nil skips the check,
// matching write-safety patterns elsewhere in this codebase), upsert the
// live sales_order_drawing row, and append one snapshot row to
// sales_order_drawing_version. Returns the saved Drawing at its new version.
func Save(ctx context.Context, pool *pgxpool.Pool, salesOrderID int, d Drawing, expectedVersion *int, actorEmployeeID *int, actorName string) (*Drawing, error) {
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

	if _, err := tx.Exec(ctx, `
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
