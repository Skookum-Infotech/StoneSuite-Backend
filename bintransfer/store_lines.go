package bintransfer

// store_lines.go — validating and writing the line set.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
)

// resolvedLine is a line validated against the yard and ready to write.
type resolvedLine struct {
	slabID    int
	fromBinID *int
	toBinID   int
	notes     string
}

// destinationBin is a bin looked up by uuid for a line's destination.
type destinationBin struct {
	id          int
	warehouseID int
	isActive    bool
	path        string
}

func lookupBin(ctx context.Context, q pgx.Tx, uuid string) (destinationBin, error) {
	var b destinationBin
	err := q.QueryRow(ctx, `
		SELECT inventory_bin_id, warehouse_id, bin_is_active, bin_path
		FROM inventory_bin
		WHERE inventory_bin_uuid = $1 AND bin_deleted_at IS NULL`, uuid).
		Scan(&b.id, &b.warehouseID, &b.isActive, &b.path)
	if errors.Is(err, pgx.ErrNoRows) || (err != nil && isInvalidText(err)) {
		return destinationBin{}, ErrNotFound
	}
	if err != nil {
		return destinationBin{}, fmt.Errorf("look up destination bin: %w", err)
	}
	return b, nil
}

// openClaim names the open bin transfer that already holds a slab, if any,
// ignoring excludeTransferID (the document being edited, whose own old lines
// must not count against it).
func openClaim(ctx context.Context, tx pgx.Tx, slabID, excludeTransferID int) (string, error) {
	var number string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(bt.bin_transfer_number, '')
		FROM bin_transfer_line l
		JOIN bin_transfer bt        ON bt.bin_transfer_id = l.bin_transfer_id
		JOIN lkp_record_status rs   ON rs.record_status_id = bt.bin_transfer_status
		WHERE l.inventory_slab_id = $1 AND l.line_deleted_at IS NULL
		  AND bt.bin_transfer_deleted_at IS NULL
		  AND bt.bin_transfer_id <> $2
		  AND rs.record_status_code IN ($3, $4, $5)
		ORDER BY bt.bin_transfer_id LIMIT 1`,
		slabID, excludeTransferID, StatusDraft, StatusPending, StatusApproved).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check open bin transfers for slab: %w", err)
	}
	return number, nil
}

// sealedBundle reports whether a unit belongs to a sealed bundle -- banded to a
// pallet, so moving one member means cutting the bands.
func sealedBundle(ctx context.Context, tx pgx.Tx, bundleID *int) (bool, error) {
	if bundleID == nil {
		return false, nil
	}
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_id = $1`, *bundleID).Scan(&status); err != nil {
		return false, fmt.Errorf("read bundle status: %w", err)
	}
	return status == "sealed", nil
}

// resolveLines validates every line against the yard and returns the rows to
// write, locking each slab so two requests cannot both claim it.
//
// This is validation at DRAFT time, for a good message. The guarantee is the
// second look Complete takes under the same row locks: a slab checked a week ago
// may have been cut, scrapped or moved since.
func resolveLines(ctx context.Context, tx pgx.Tx, warehouseID, excludeTransferID int, in []LineInput) ([]resolvedLine, error) {
	out := make([]resolvedLine, 0, len(in))
	for i, li := range in {
		n := i + 1
		unit, err := inventory.ResolveUnitForDocument(ctx, tx, li.UnitID, true)
		if errors.Is(err, inventory.ErrNotFound) {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d names a slab that does not exist.", n)}
		}
		if err != nil {
			return nil, err
		}
		if unit.WarehouseID != warehouseID {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: slab %s is not in this location.", n, unit.Serial)}
		}
		switch unit.Status {
		case inventory.StatusConsumed, inventory.StatusScrapped, inventory.StatusInTransit:
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: slab %s is %s and cannot be moved.",
				n, unit.Serial, strings.ReplaceAll(unit.Status, "_", " "))}
		}
		sealed, err := sealedBundle(ctx, tx, unit.BundleID)
		if err != nil {
			return nil, err
		}
		if sealed {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: slab %s is in a sealed bundle. Move the whole bundle, or break it first.", n, unit.Serial)}
		}

		bin, err := lookupBin(ctx, tx, li.ToBinID)
		if errors.Is(err, ErrNotFound) {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d names a bin that does not exist.", n)}
		}
		if err != nil {
			return nil, err
		}
		if bin.warehouseID != warehouseID {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: the destination bin is in a different location.", n)}
		}
		if !bin.isActive {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: bin %s is inactive.", n, bin.path)}
		}
		if unit.BinID != nil && *unit.BinID == bin.id {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: slab %s is already in that bin.", n, unit.Serial)}
		}

		held, err := openClaim(ctx, tx, unit.ID, excludeTransferID)
		if err != nil {
			return nil, err
		}
		if held != "" {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: slab %s is already on bin transfer %s.", n, unit.Serial, held)}
		}

		out = append(out, resolvedLine{slabID: unit.ID, fromBinID: unit.BinID, toBinID: bin.id, notes: li.Notes})
	}
	return out, nil
}

// replaceLines soft-deletes the live line set and writes the new one.
func replaceLines(ctx context.Context, tx pgx.Tx, transferID int, lines []resolvedLine, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE bin_transfer_line SET line_deleted_at = NOW()
		WHERE bin_transfer_id = $1 AND line_deleted_at IS NULL`, transferID); err != nil {
		return fmt.Errorf("clear bin transfer lines: %w", err)
	}
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO bin_transfer_line (
				bin_transfer_id, line_number, inventory_slab_id, from_bin_id, to_bin_id,
				line_notes, line_created_by
			) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			transferID, i+1, l.slabID, l.fromBinID, l.toBinID, l.notes, nullableInt(actorEmployeeID)); err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d references a slab or bin that no longer exists.", i+1)}
			}
			return fmt.Errorf("insert bin transfer line: %w", err)
		}
	}
	return nil
}
