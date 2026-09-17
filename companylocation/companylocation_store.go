package companylocation

// companylocation_store.go — CRUD for company_location, mirroring
// inventory/warehouse_store.go's "multiple rows, exactly one flagged
// default" shape, with two deliberate departures:
//
//   - Create auto-flags a tenant's very first location as default (in the
//     same INSERT, via a NOT EXISTS subquery) — every location after that
//     starts non-default. Warehouses never auto-default on create because a
//     tenant's warehouse list is seeded with a system default already; a
//     tenant's location list starts empty, and requiring a separate
//     "now set it default" step right after adding your only location would
//     leave the Locations tab looking like nothing was saved.
//   - Delete only blocks on the default when another live location would be
//     left defaultless ("make another the default first"). Deleting a
//     tenant's one and only location is allowed — that just returns them to
//     the zero-locations fallback (Company Profile's billing address).

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const locationSelect = `
	SELECT company_location_uuid, name, phone,
	       addr_line1, addr_line2, addr_suite, addr_city, addr_country, addr_state, addr_zip,
	       is_default
	FROM company_location`

func scanLocation(row pgx.Row) (*Location, error) {
	var l Location
	if err := row.Scan(&l.ID, &l.Name, &l.Phone,
		&l.Address.Line1, &l.Address.Line2, &l.Address.Suite, &l.Address.City, &l.Address.Country, &l.Address.State, &l.Address.Zip,
		&l.IsDefault); err != nil {
		return nil, err
	}
	return &l, nil
}

// List returns every live location, default first.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Location, error) {
	rows, err := pool.Query(ctx, locationSelect+`
		WHERE deleted_at IS NULL
		ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list company locations: %w", err)
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan company location: %w", err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list company locations: %w", err)
	}
	return out, nil
}

// Get loads one live location by uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Location, error) {
	l, err := scanLocation(pool.QueryRow(ctx, locationSelect+`
		WHERE company_location_uuid = $1 AND deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get company location: %w", err)
	}
	return l, nil
}

// Create inserts a location. It becomes the default only if it is the
// tenant's first live location — see the package doc comment above.
func Create(ctx context.Context, pool *pgxpool.Pool, in Input, actorEmployeeID int) (*Location, error) {
	if err := Validate(in); err != nil {
		return nil, err
	}
	var newID string
	err := pool.QueryRow(ctx, `
		INSERT INTO company_location (
			name, phone, addr_line1, addr_line2, addr_suite, addr_city, addr_country, addr_state, addr_zip,
			is_default, created_by
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,
			NOT EXISTS (SELECT 1 FROM company_location WHERE deleted_at IS NULL),
			$10
		)
		RETURNING company_location_uuid`,
		in.Name, in.Phone, in.Address.Line1, in.Address.Line2, in.Address.Suite, in.Address.City, in.Address.Country, in.Address.State, in.Address.Zip,
		nullableInt(actorEmployeeID),
	).Scan(&newID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ClientError{"Another location was just added as the default — please try again."}
		}
		return nil, fmt.Errorf("insert company location: %w", err)
	}
	return Get(ctx, pool, newID)
}

// Update overwrites a location's editable fields. is_default is untouched —
// SetDefault is the only way to move it.
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in Input) error {
	if err := Validate(in); err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `
		UPDATE company_location SET
			name = $2, phone = $3,
			addr_line1 = $4, addr_line2 = $5, addr_suite = $6, addr_city = $7, addr_country = $8, addr_state = $9, addr_zip = $10,
			updated_at = NOW()
		WHERE company_location_uuid = $1 AND deleted_at IS NULL`,
		id, in.Name, in.Phone, in.Address.Line1, in.Address.Line2, in.Address.Suite, in.Address.City, in.Address.Country, in.Address.State, in.Address.Zip)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("update company location: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDefault moves the default flag.
//
// Clearing the old default and setting the new one MUST happen in one
// transaction: uq_company_location_default is a partial unique index over
// is_default = TRUE, so two rows briefly holding it — which is what two
// separate statements would produce — is rejected outright.
func SetDefault(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set default company location: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pk int
	err = tx.QueryRow(ctx, `
		SELECT company_location_id FROM company_location
		WHERE company_location_uuid = $1 AND deleted_at IS NULL`, id).Scan(&pk)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("resolve company location: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE company_location SET is_default = FALSE, updated_at = NOW()
		WHERE is_default = TRUE AND company_location_id <> $1`, pk); err != nil {
		return fmt.Errorf("clear previous default company location: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE company_location SET is_default = TRUE, updated_at = NOW() WHERE company_location_id = $1`, pk); err != nil {
		return fmt.Errorf("set default company location: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit set default company location: %w", err)
	}
	return nil
}

// Delete soft-deletes a location. Blocked on the current default only when
// another live location would be left behind — see the package doc comment.
func Delete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete company location: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		pk        int
		isDefault bool
	)
	err = tx.QueryRow(ctx, `
		SELECT company_location_id, is_default FROM company_location
		WHERE company_location_uuid = $1 AND deleted_at IS NULL`, id).Scan(&pk, &isDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("resolve company location: %w", err)
	}

	if isDefault {
		var otherLiveCount int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM company_location
			WHERE deleted_at IS NULL AND company_location_id <> $1`, pk).Scan(&otherLiveCount); err != nil {
			return fmt.Errorf("count other company locations: %w", err)
		}
		if otherLiveCount > 0 {
			return ClientError{"Make another location the default before deleting this one."}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE company_location SET deleted_at = NOW(), deleted_by = $2
		WHERE company_location_id = $1`, pk, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete company location: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete company location: %w", err)
	}
	return nil
}
