package inventory

// store.go — shared item-store helpers plus the single-row read.
// The write verbs live in store_create.go / store_update.go / store_delete.go
// and the list path in store_search.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// validateItemInput applies the input rules shared by Create and Update.
// Referential validity (unit, material, colour, finish, warehouse) is left to
// the foreign keys and surfaced by mapItemWriteErr, so there is exactly one
// source of truth for what a valid reference is.
func validateItemInput(in *CreateItemInput) error {
	if strings.TrimSpace(in.SKU) == "" {
		return ClientError{Msg: "SKU is required."}
	}
	if strings.TrimSpace(in.Name) == "" {
		return ClientError{Msg: "Name is required."}
	}
	if in.UnitID <= 0 {
		return ClientError{Msg: "A unit of measure is required."}
	}
	if in.UnitPrice < 0 {
		return ClientError{Msg: "Unit price cannot be negative."}
	}
	if in.ThicknessMM < 0 {
		return ClientError{Msg: "Thickness cannot be negative."}
	}
	// Default rather than reject: every row that existed before this column was
	// added is 'quantity', and an omitted field on an existing client must keep
	// meaning the same thing.
	if in.Tracking == "" {
		in.Tracking = TrackingQuantity
	}
	if in.Tracking != TrackingQuantity && in.Tracking != TrackingSerialized {
		return ClientError{Msg: "Tracking must be either 'quantity' or 'serialized'."}
	}
	return nil
}

// mapItemWriteErr turns a PostgreSQL constraint violation from an item write
// into a client-facing 400 where one is warranted, and otherwise wraps.
func mapItemWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		// Two partial unique indexes can fire here. Distinguishing them by
		// message would mean parsing the constraint name; both are the user
		// re-using an identifier, so one clear message covers it.
		return ClientError{Msg: "An active item with this SKU or barcode already exists."}
	case isFKViolation(err):
		return ClientError{Msg: "Unknown unit, material, colour, finish, country or warehouse."}
	case isCheckViolation(err):
		return ClientError{Msg: "One or more item fields failed validation."}
	}
	return fmt.Errorf("%s inventory item: %w", verb, err)
}

// validateCustom validates custom fields against the "inventory_item"
// workflow's field definitions when one is configured; inventory_item is a
// dedicated relational domain (not a v1 JSONB workflow), so in practice no
// such workflow exists and validation is a no-op — kept for parity with
// crmstore.validateCustom and to support a future admin-configured field set.
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "inventory_item")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return err
	}
	if custom == nil {
		return nil
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// Get loads a single live inventory item by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Item, error) {
	it, err := scanItem(pool.QueryRow(ctx, itemSelect+`
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		// An unparseable uuid reaches the driver as a type error rather than
		// ErrNoRows. Treating it as not-found keeps a malformed path segment
		// from becoming a 500 and matches the 404-on-denial rule.
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory item: %w", err)
	}
	return it, nil
}

// isInvalidTextRepresentation reports whether err is SQLSTATE 22P02 — the code
// PostgreSQL returns for a malformed uuid literal.
func isInvalidTextRepresentation(err error) bool { return sqlState(err, "22P02") }

// itemIDByUUID resolves an external uuid to the internal SERIAL id inside a
// transaction, returning ErrNotFound when the row is missing or soft-deleted.
func itemIDByUUID(ctx context.Context, q pgxQuerier, uuid string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT inventory_item_id FROM inventory_item
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("resolve inventory item: %w", err)
	}
	return id, nil
}

// pgxQuerier is the narrow read interface satisfied by both *pgxpool.Pool and
// pgx.Tx, so helpers work inside or outside a transaction. Defined at the point
// of use, per the repo's interface rule.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
