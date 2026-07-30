package inventory

// count_freeze.go — the freeze a cycle count puts on the stock it is counting.
//
// A count's variance is measured against the system quantity snapshotted when
// counting STARTED. If stock keeps moving while the crew walks the yard, the
// variance stops meaning anything: a slab moved from a counted bin to an
// uncounted one shows as a shortage in the first and a surprise in the second,
// and the count posts two write-offs for stone that never went anywhere.
//
// The freeze runs from the moment a count is frozen until it posts or is
// cancelled. Keyed on those timestamps rather than the status code so the guard
// needs no join to lkp_record_status on the hot path, and so a count parked in
// review still holds its freeze — its variances are not yet reconciled.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// FrozenByCountError reports that a live count covers the stock being moved.
// It is a ClientError so controllers map it to 400 with the count's number in
// the message — "which count is blocking me" is the first thing the yard asks.
type FrozenByCountError struct {
	CountNumber string
}

func (e FrozenByCountError) Error() string {
	return fmt.Sprintf("frozen by count %s", e.CountNumber)
}

// countFreezeSQL finds a live count whose scope covers (warehouse, binPath).
//
// A count with no bin covers its whole warehouse. A count scoped to a bin
// covers that bin and everything beneath it, matched on the materialised
// bin_path so the subtree is one prefix comparison rather than a recursive walk.
//
// binPath is empty for a unit that sits in no bin. Such a unit is inside a
// warehouse-wide count but outside any bin-scoped one, which the predicate
// expresses by requiring a non-empty path for the subtree branch.
const countFreezeSQL = `
	SELECT COALESCE(c.count_number, 'ICNT-draft')
	FROM inventory_count c
	LEFT JOIN inventory_bin cb ON cb.inventory_bin_id = c.inventory_bin_id
	WHERE c.count_deleted_at   IS NULL
	  AND c.count_frozen_at    IS NOT NULL
	  AND c.count_posted_at    IS NULL
	  AND c.count_cancelled_at IS NULL
	  AND c.warehouse_id = $1
	  AND (c.inventory_bin_id IS NULL
	       OR ($2 <> '' AND ($2 = cb.bin_path OR $2 LIKE cb.bin_path || '/%')))
	LIMIT 1`

// checkNotFrozenByCount refuses a movement inside a live count's scope.
func checkNotFrozenByCount(ctx context.Context, q pgxQuerier, warehouseID int, binPath string) error {
	var number string
	err := q.QueryRow(ctx, countFreezeSQL, warehouseID, binPath).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check count freeze: %w", err)
	}
	return ClientError{Msg: fmt.Sprintf(
		"Cycle count %s is counting this location. Post or cancel it before moving stock here.", number)}
}

// CheckNotFrozenByCount is the exported form, for the document modules.
func CheckNotFrozenByCount(ctx context.Context, q pgxQuerier, warehouseID int, binPath string) error {
	return checkNotFrozenByCount(ctx, q, warehouseID, binPath)
}

// checkBinMoveNotFrozen guards BOTH ends of a bin move. Checking only the
// source would still allow stock to be moved into a frozen bin, which corrupts
// that bin's count exactly as much as moving stock out of it.
func checkBinMoveNotFrozen(ctx context.Context, q pgxQuerier, warehouseID int, fromBin, toBin *int) error {
	for _, bin := range []*int{fromBin, toBin} {
		path, err := binPathByID(ctx, q, bin)
		if err != nil {
			return err
		}
		if err := checkNotFrozenByCount(ctx, q, warehouseID, path); err != nil {
			return err
		}
	}
	return nil
}

// binPathByID resolves a bin's materialised path, used when a movement's target
// is given as an id rather than a loaded row.
func binPathByID(ctx context.Context, q pgxQuerier, binID *int) (string, error) {
	if binID == nil {
		return "", nil
	}
	var path string
	err := q.QueryRow(ctx, `
		SELECT bin_path FROM inventory_bin
		WHERE inventory_bin_id = $1 AND bin_deleted_at IS NULL`, *binID).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve bin path: %w", err)
	}
	return path, nil
}
