package inventory

// unit_reservation.go — holding one slab for a customer straight from Inventory.
//
// This is the lightweight sibling of a fabrication allocation: no sales order, no
// job, just "this stone is spoken for". The slab goes to status 'reserved' and
// remembers the customer (inventory_slab.reserved_customer_id). Stock is not
// touched — a reserved slab is still on hand, exactly as one a job holds is — and
// nothing that allocates stone (a job, a transfer, a cut) will take it while it is
// reserved. Releasing it puts it back to 'available'.
//
// Only an available slab can be reserved: one still waiting for a bin ('received')
// has not been put away, and one on order has not arrived.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// StatusOnArrival is the status a slab takes when a receipt brings it in: available
// when it was put straight into a bin, received while it has none yet.
func StatusOnArrival(binID *int) string {
	if binID == nil {
		return StatusReceived
	}
	return StatusAvailable
}

// ReserveUnitForCustomer reserves one available slab for a customer.
func ReserveUnitForCustomer(ctx context.Context, pool *pgxpool.Pool, unitUUID, customerUUID, note string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reserve unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, err := unitByUUID(ctx, tx, unitUUID, true)
	if err != nil {
		return err
	}
	switch u.status {
	case StatusAvailable:
	case StatusReserved:
		return ClientError{Msg: "This unit is already reserved."}
	case StatusReceived:
		return ClientError{Msg: "This unit has not been put in a bin yet. Move it to a bin before reserving it."}
	case StatusOnOrder:
		return ClientError{Msg: "This unit is still on order and has not arrived, so it cannot be reserved."}
	case StatusInTransit:
		return ClientError{Msg: "This unit is in transit. Receive its transfer before reserving it."}
	default:
		return ClientError{Msg: "A consumed or scrapped unit cannot be reserved."}
	}

	var customerID int
	var customerName string
	err = tx.QueryRow(ctx, `
		SELECT customer_id, customer_name FROM customer
		WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, strings.TrimSpace(customerUUID)).Scan(&customerID, &customerName)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidTextRepresentation(err) {
		return ClientError{Msg: "Choose a customer to reserve this unit for."}
	}
	if err != nil {
		return fmt.Errorf("resolve customer to reserve for: %w", err)
	}
	if msg, err := workflow.CustomerNotUsableByUUID(ctx, tx, strings.TrimSpace(customerUUID)); err != nil {
		return err
	} else if msg != "" {
		return ClientError{Msg: msg}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab
		SET slab_status = $2, reserved_customer_id = $3, reserved_at = NOW(), reserved_by = $4,
		    slab_updated_at = NOW(), slab_updated_by = $4
		WHERE inventory_slab_id = $1`,
		u.id, StatusReserved, customerID, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "reserve")
	}
	if err := writeUnitHistory(ctx, tx, u.id, "status_change", "status", StatusAvailable, StatusReserved,
		u.binID, u.binID, nil, reservationNote("Reserved for "+customerName+".", note), actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reserve unit: %w", err)
	}
	return nil
}

// ReleaseUnitReservation gives a slab held for a customer back to available. A
// reservation a fabrication job holds is not this one's to release — that goes
// through the job.
func ReleaseUnitReservation(ctx context.Context, pool *pgxpool.Pool, unitUUID, note string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin release unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, err := unitByUUID(ctx, tx, unitUUID, true)
	if err != nil {
		return err
	}
	var customerName *string
	if u.status == StatusReserved {
		err = tx.QueryRow(ctx, `
			SELECT c.customer_name FROM inventory_slab s
			JOIN customer c ON c.customer_id = s.reserved_customer_id
			WHERE s.inventory_slab_id = $1`, u.id).Scan(&customerName)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read unit reservation: %w", err)
		}
	}
	if u.status != StatusReserved || customerName == nil {
		return ClientError{Msg: "This unit is not reserved for a customer. A reservation a fabrication job holds is released from the job."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab
		SET slab_status = $2, reserved_customer_id = NULL, reserved_at = NULL, reserved_by = NULL,
		    slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`,
		u.id, StatusAvailable, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "release")
	}
	if err := writeUnitHistory(ctx, tx, u.id, "status_change", "status", StatusReserved, StatusAvailable,
		u.binID, u.binID, nil, reservationNote("Released from "+*customerName+".", note), actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit release unit: %w", err)
	}
	return nil
}

// reservationNote joins the system's line and the user's own note.
func reservationNote(system, user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return system
	}
	return system + " " + user
}
