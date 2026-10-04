package inventory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CutUnitToBinsTx requires one explicit destination per remnant, in input order.
// Destinations must be active bins in the parent's warehouse. The caller owns
// commit and must roll back on errors. Reserved units remain prohibited.
func CutUnitToBinsTx(ctx context.Context, tx pgx.Tx, uuid string, in CutInput, destinations []string, actorEmployeeID int) (CutTxResult, error) {
	if len(destinations) != len(in.Remnants) {
		return CutTxResult{}, ClientError{Msg: "Choose a destination bin for every remnant."}
	}
	for _, destination := range destinations {
		if strings.TrimSpace(destination) == "" {
			return CutTxResult{}, ClientError{Msg: "Choose a destination bin for every remnant."}
		}
	}
	explicit := make([]string, len(destinations))
	copy(explicit, destinations)
	return cutUnitTx(ctx, tx, uuid, in, explicit, actorEmployeeID)
}

func cutDestination(ctx context.Context, tx pgx.Tx, uuid string, warehouseID int, source *int) (*int, error) {
	var id, warehouse int
	var active bool
	err := tx.QueryRow(ctx, `SELECT inventory_bin_id, warehouse_id, bin_is_active
 FROM inventory_bin WHERE inventory_bin_uuid=$1 AND bin_deleted_at IS NULL
 FOR SHARE`, uuid).Scan(&id, &warehouse, &active)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidTextRepresentation(err) {
		return nil, ClientError{Msg: "Choose an active destination bin."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve remnant destination: %w", err)
	}
	if !active {
		return nil, ClientError{Msg: "Choose an active destination bin."}
	}
	if warehouse != warehouseID {
		return nil, ClientError{Msg: "The remnant destination must be in the slab's location."}
	}
	if err := checkBinMoveNotFrozen(ctx, tx, warehouseID, source, &id); err != nil {
		return nil, err
	}
	return &id, nil
}
