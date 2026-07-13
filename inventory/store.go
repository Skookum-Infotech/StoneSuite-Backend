package inventory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an inventory item uuid matches nothing live
// (or is soft-deleted).
var ErrNotFound = errors.New("inventory item not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring crmstore.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (code 23505), mirroring controllers.isUniqueViolation (kept local
// to avoid a store->controllers import).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const itemSelect = `
	SELECT inventory_item_uuid, inventory_item_sku, inventory_item_name, inventory_item_description,
	       inventory_item_unit_id, inventory_item_unit_price, inventory_item_currency_id, inventory_item_tax_rate_id,
	       inventory_item_is_active, inventory_item_custom_fields,
	       inventory_item_created_at, inventory_item_updated_at
	FROM inventory_item`

func scanItem(row pgx.Row) (*Item, error) {
	var (
		it     Item
		custom map[string]any
	)
	if err := row.Scan(
		&it.ID, &it.SKU, &it.Name, &it.Description,
		&it.UnitID, &it.UnitPrice, &it.CurrencyID, &it.TaxRateID,
		&it.IsActive, &custom,
		&it.CreatedAt, &it.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if custom == nil {
		custom = map[string]any{}
	}
	it.CustomFields = custom
	return &it, nil
}

// nullableInt converts a non-positive employee id to SQL NULL, matching
// crmstore's nullableInt convention (employee id 0/unresolved => NULL).
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
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

// Create inserts a new inventory item. SKU uniqueness (case-insensitive,
// among live rows) is enforced by uq_inventory_item_sku_active.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateItemInput, actorEmployeeID int) (*Item, error) {
	if strings.TrimSpace(in.SKU) == "" {
		return nil, ClientError{Msg: "SKU is required."}
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, ClientError{Msg: "Name is required."}
	}
	if in.UnitID <= 0 {
		return nil, ClientError{Msg: "A unit of measure is required."}
	}
	if in.UnitPrice < 0 {
		return nil, ClientError{Msg: "Unit price cannot be negative."}
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}
	var newUUID string
	err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (
			inventory_item_sku, inventory_item_name, inventory_item_description,
			inventory_item_unit_id, inventory_item_unit_price, inventory_item_currency_id,
			inventory_item_tax_rate_id, inventory_item_custom_fields, inventory_item_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING inventory_item_uuid`,
		in.SKU, in.Name, in.Description, in.UnitID, in.UnitPrice,
		in.CurrencyID, in.TaxRateID, custom, nullableInt(actorEmployeeID),
	).Scan(&newUUID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ClientError{Msg: "An active item with this SKU already exists."}
		}
		return nil, fmt.Errorf("insert inventory item: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// Get loads a single live inventory item by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Item, error) {
	it, err := scanItem(pool.QueryRow(ctx, itemSelect+`
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get inventory item: %w", err)
	}
	return it, nil
}

// Update overwrites an item's editable fields in place (SKU included — the
// active-row unique index still guards collisions).
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in CreateItemInput, actorEmployeeID int) error {
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
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `
		UPDATE inventory_item SET
			inventory_item_sku = $2, inventory_item_name = $3, inventory_item_description = $4,
			inventory_item_unit_id = $5, inventory_item_unit_price = $6,
			inventory_item_currency_id = $7, inventory_item_tax_rate_id = $8,
			inventory_item_custom_fields = $9,
			inventory_item_updated_at = NOW(), inventory_item_updated_by = $10,
			inventory_item_record_version = inventory_item_record_version + 1
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL`,
		uuid, in.SKU, in.Name, in.Description, in.UnitID, in.UnitPrice,
		in.CurrencyID, in.TaxRateID, custom, nullableInt(actorEmployeeID))
	if err != nil {
		if isUniqueViolation(err) {
			return ClientError{Msg: "An active item with this SKU already exists."}
		}
		return fmt.Errorf("update inventory item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDelete marks an item deleted; it is excluded from Get/Search thereafter
// but existing sales_order_item snapshots (which do not cascade) still
// reference it for historical display.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE inventory_item
		SET inventory_item_deleted_at = NOW(), inventory_item_deleted_by = $2
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete inventory item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Page is one page of a keyset-paginated item search.
type Page struct {
	Records    []Item
	NextCursor string
	HasMore    bool
}

// Search lists live inventory items with filter/sort/global-search + keyset
// pagination via the shared query engine. Inventory is tenant-global
// reference data (like lookups), so unlike Sales Order this has no per-row
// RBAC scope to AND in — only the resource-level inventory_item:read grant
// checked by the caller.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request) (Page, error) {
	built, err := query.Build(req, resolver{}, 1)
	if err != nil {
		return Page{}, err
	}
	where := "inventory_item_deleted_at IS NULL"
	if built.Where != "" {
		where += " AND " + built.Where
	}
	if built.Keyset != "" {
		where += " AND " + built.Keyset
	}
	q := itemSelect + " WHERE " + where +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return Page{}, fmt.Errorf("search inventory items: %w", err)
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search inventory items: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from an item to mint the
// next cursor.
func sortValue(it Item, field string) any {
	switch field {
	case "updated_at":
		return it.UpdatedAt
	case "sku":
		return it.SKU
	case "name":
		return it.Name
	default: // created_at (default)
		return it.CreatedAt
	}
}
