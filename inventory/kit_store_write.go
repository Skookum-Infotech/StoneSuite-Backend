package inventory

// kit_store_write.go — creating, editing and deleting kits. Membership is
// always replaced as a whole inside the kit's own transaction, so a kit can
// never be observed half-written.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/duplicate"
)

// validateKitInput applies the rules shared by create and update and tidies the
// input in place: trimmed name, lower-cased item ids, a default quantity of 1.
func validateKitInput(in *KitInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return ClientError{Msg: "A kit name is required."}
	}
	if utf8.RuneCountInString(in.Name) > MaxKitNameLen {
		return ClientError{Msg: fmt.Sprintf("A kit name can be at most %d characters.", MaxKitNameLen)}
	}
	if len(in.Members) > MaxKitMembers {
		return ClientError{Msg: fmt.Sprintf("A kit can hold at most %d items.", MaxKitMembers)}
	}
	seen := make(map[string]bool, len(in.Members))
	for i := range in.Members {
		m := &in.Members[i]
		m.ItemID = strings.ToLower(strings.TrimSpace(m.ItemID))
		if m.ItemID == "" {
			return ClientError{Msg: "Every kit item needs an item."}
		}
		if seen[m.ItemID] {
			return ClientError{Msg: "An item can appear in a kit only once."}
		}
		seen[m.ItemID] = true
		switch {
		case m.Quantity == 0:
			m.Quantity = 1
		case m.Quantity < 0:
			return ClientError{Msg: "A kit item's quantity must be greater than zero."}
		}
	}
	return nil
}

// mapKitWriteErr turns a PostgreSQL constraint violation from a kit write into a
// client-facing 400 where one is warranted, and otherwise wraps.
func mapKitWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "A kit with this name already exists."}
	case isFKViolation(err):
		return ClientError{Msg: "One of the kit's items no longer exists."}
	case isCheckViolation(err):
		return ClientError{Msg: "One or more kit fields failed validation."}
	case isInvalidTextRepresentation(err):
		return ClientError{Msg: "One of the kit's items is not a valid item."}
	}
	return fmt.Errorf("%s inventory kit: %w", verb, err)
}

// CreateKit inserts a kit and its members and returns it as read back.
func CreateKit(ctx context.Context, pool *pgxpool.Pool, in KitInput, actorEmployeeID int) (*Kit, error) {
	if err := validateKitInput(&in); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create inventory kit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := checkKitNameFree(ctx, tx, in.Name, ""); err != nil {
		return nil, err
	}
	active := in.IsActive == nil || *in.IsActive

	var (
		kitID   int
		newUUID string
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_kit (
			inventory_kit_name, inventory_kit_description, inventory_kit_is_active, inventory_kit_created_by
		) VALUES ($1,$2,$3,$4)
		RETURNING inventory_kit_id, inventory_kit_uuid::text`,
		in.Name, in.Description, active, nullableInt(actorEmployeeID),
	).Scan(&kitID, &newUUID)
	if err != nil {
		return nil, mapKitWriteErr(err, "insert")
	}
	if err := replaceKitMembers(ctx, tx, kitID, in.Members); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create inventory kit: %w", err)
	}
	return GetKit(ctx, pool, newUUID)
}

// UpdateKit overwrites a kit's fields. Members are replaced only when the input
// carries a list (an empty one empties the kit); a nil list leaves them alone.
func UpdateKit(ctx context.Context, pool *pgxpool.Pool, uuid string, in KitInput, actorEmployeeID int) error {
	if err := validateKitInput(&in); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update inventory kit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		kitID      int
		beforeName string
	)
	err = tx.QueryRow(ctx, `
		SELECT inventory_kit_id, inventory_kit_name FROM inventory_kit
		WHERE inventory_kit_uuid = $1 AND inventory_kit_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&kitID, &beforeName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("lock inventory kit: %w", err)
	}

	// Only a real rename is checked, so a kit that shares its name with another
	// can still be edited for anything else.
	if duplicate.Key(in.Name) != duplicate.Key(beforeName) {
		if err := checkKitNameFree(ctx, tx, in.Name, uuid); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_kit SET
			inventory_kit_name = $2, inventory_kit_description = $3,
			inventory_kit_is_active = COALESCE($4, inventory_kit_is_active),
			inventory_kit_updated_at = NOW(), inventory_kit_updated_by = $5,
			inventory_kit_record_version = inventory_kit_record_version + 1
		WHERE inventory_kit_id = $1`,
		kitID, in.Name, in.Description, in.IsActive, nullableInt(actorEmployeeID)); err != nil {
		return mapKitWriteErr(err, "update")
	}
	if in.Members != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM inventory_kit_item WHERE inventory_kit_id = $1`, kitID); err != nil {
			return fmt.Errorf("clear kit members: %w", err)
		}
		if err := replaceKitMembers(ctx, tx, kitID, in.Members); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update inventory kit: %w", err)
	}
	return nil
}

// SoftDeleteKit marks a kit deleted. It disappears from every list and picker
// and its name is free again; documents that were built from it keep the name
// they froze, so nothing already saved changes.
func SoftDeleteKit(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE inventory_kit
		SET inventory_kit_deleted_at = NOW(), inventory_kit_deleted_by = $2
		WHERE inventory_kit_uuid = $1 AND inventory_kit_deleted_at IS NULL`,
		uuid, actorOrSystem(actorEmployeeID))
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("delete inventory kit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// replaceKitMembers inserts a kit's members in list order, resolving each item's
// external uuid to its internal id in one query. Every id must name a live item;
// the caller has already emptied the kit when this is an edit.
func replaceKitMembers(ctx context.Context, tx pgx.Tx, kitID int, members []KitMemberInput) error {
	if len(members) == 0 {
		return nil
	}
	uuids := make([]string, len(members))
	for i, m := range members {
		uuids[i] = m.ItemID
	}
	rows, err := tx.Query(ctx, `
		SELECT inventory_item_uuid::text, inventory_item_id FROM inventory_item
		WHERE inventory_item_uuid::text = ANY($1::text[]) AND inventory_item_deleted_at IS NULL`, uuids)
	if err != nil {
		return fmt.Errorf("resolve kit items: %w", err)
	}
	ids := make(map[string]int, len(members))
	for rows.Next() {
		var uuid string
		var id int
		if err := rows.Scan(&uuid, &id); err != nil {
			rows.Close()
			return fmt.Errorf("scan kit item: %w", err)
		}
		ids[uuid] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("resolve kit items: %w", err)
	}

	for pos, m := range members {
		itemID, ok := ids[m.ItemID]
		if !ok {
			return ClientError{Msg: "One of the kit's items no longer exists."}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory_kit_item (inventory_kit_id, inventory_item_id, kit_item_quantity, kit_item_position)
			VALUES ($1,$2,$3,$4)`, kitID, itemID, m.Quantity, pos); err != nil {
			// validateKitInput already refused a repeated item; this is the
			// backstop for two ids that resolve to the same one.
			if isUniqueViolation(err) {
				return ClientError{Msg: "An item can appear in a kit only once."}
			}
			return mapKitWriteErr(err, "add item to")
		}
	}
	return nil
}
