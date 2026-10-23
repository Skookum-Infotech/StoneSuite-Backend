package proposedentity

import (
	"context"
	"fmt"

	"stonesuite-backend/docextract"
	"stonesuite-backend/workflow"
)

// defaultUnitCode is the lkp_unit code used when the document UoM is unknown.
const defaultUnitCode = "EA"

type unitRow struct {
	id   int
	code string
	name string
}

// ResolveUnit maps a document unit-of-measure spelling to an active lkp_unit
// row. Unknown or empty text falls back to 'EA'; it errors only when EA itself
// is missing.
func ResolveUnit(ctx context.Context, q workflow.Querier, docUoM string) (int, string, error) {
	rows, err := q.Query(ctx, `
		SELECT unit_id, unit_code, unit_name FROM lkp_unit
		WHERE unit_deleted_at IS NULL AND unit_is_active ORDER BY unit_id`)
	if err != nil {
		return 0, "", fmt.Errorf("load units: %w", err)
	}
	defer rows.Close()
	var units []unitRow
	for rows.Next() {
		var u unitRow
		if err := rows.Scan(&u.id, &u.code, &u.name); err != nil {
			return 0, "", fmt.Errorf("scan unit: %w", err)
		}
		units = append(units, u)
	}
	if err := rows.Err(); err != nil {
		return 0, "", fmt.Errorf("load units: %w", err)
	}
	return pickUnit(units, docUoM)
}

// pickUnit chooses the unit whose normalized code (then name) equals the
// canonical form of docUoM, else EA.
func pickUnit(units []unitRow, docUoM string) (int, string, error) {
	if canon, _, ok := docextract.NormalizeUnit(docUoM); ok {
		for _, u := range units {
			if c, _, ok := docextract.NormalizeUnit(u.code); ok && c == canon {
				return u.id, u.code, nil
			}
		}
		for _, u := range units {
			if c, _, ok := docextract.NormalizeUnit(u.name); ok && c == canon {
				return u.id, u.code, nil
			}
		}
	}
	for _, u := range units {
		if u.code == defaultUnitCode {
			return u.id, u.code, nil
		}
	}
	return 0, "", fmt.Errorf("default unit %s is missing", defaultUnitCode)
}
