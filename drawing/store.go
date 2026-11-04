package drawing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// snapshot is the full saved document, serialized as one JSONB blob into
// sales_order_drawing_version.snapshot. The live sales_order_drawing row
// keeps the same fields as separate columns (B1) so the current drawing can
// be read/updated without a JSON round-trip on every field.
type snapshot struct {
	SchemaVersion       int    `json:"schemaVersion"`
	DisplayUnit         string `json:"displayUnit"`
	FractionDenominator int    `json:"fractionDenominator"`
	Areas               []Area `json:"areas"`
}

// isoTime formats t the same way the frontend's Date#toISOString() does,
// matching the Drawing.updatedAt string the client already expects.
func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func updatedByFrom(actorEmployeeID *int, firstName, lastName *string) UpdatedBy {
	if actorEmployeeID == nil {
		return UpdatedBy{}
	}
	name := strings.TrimSpace(deref(firstName) + " " + deref(lastName))
	return UpdatedBy{ID: strconv.Itoa(*actorEmployeeID), Name: name}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Get returns the current saved Drawing for a sales order, or a
// ErrNotFound-wrapped error if none has been saved yet.
func Get(ctx context.Context, pool *pgxpool.Pool, salesOrderID int) (*Drawing, error) {
	var (
		d                     Drawing
		areasRaw              []byte
		actorEmployeeID       *int
		actorFirst, actorLast *string
		updatedAt             time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT d.schema_version, d.display_unit, d.fraction_denominator, d.areas,
		       d.drawing_record_version, d.updated_at, d.actor_employee_id,
		       e.employee_first_name, e.employee_last_name
		FROM sales_order_drawing d
		LEFT JOIN employee e ON e.employee_id = d.actor_employee_id
		WHERE d.sales_order_id = $1`, salesOrderID).Scan(
		&d.SchemaVersion, &d.DisplayUnit, &d.FractionDenominator, &areasRaw,
		&d.Version, &updatedAt, &actorEmployeeID, &actorFirst, &actorLast)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("drawing: get: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("drawing: get: %w", err)
	}
	if err := json.Unmarshal(areasRaw, &d.Areas); err != nil {
		return nil, fmt.Errorf("drawing: get: decode areas: %w", err)
	}
	d.UpdatedAt = isoTime(updatedAt)
	d.UpdatedBy = updatedByFrom(actorEmployeeID, actorFirst, actorLast)
	return &d, nil
}
