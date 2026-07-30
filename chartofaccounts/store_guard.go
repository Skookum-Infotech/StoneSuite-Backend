package chartofaccounts

import (
	"context"
	"fmt"
	"strings"
)

// BlockingSlots returns the default-mapping slot keys pointing at accountID.
//
// This is the single referential guard (AD-7). Deactivating, hiding,
// soft-deleting, or un-posting an account a slot points at would leave the
// slot dangling -- nothing would error until a transaction failed months
// later. All four mutations call guardRetire, which calls this. Writing it
// once with four callers, rather than four independent checks, is precisely
// because the four-independent-checks version is how three of them end up
// missing it.
func BlockingSlots(ctx context.Context, q rowQuerier, accountID int) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT slot_key FROM coa_default_mapping
		WHERE coa_account_id = $1 ORDER BY slot_sort_order`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list blocking slots: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan blocking slot: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocking slots: %w", err)
	}
	return out, nil
}

// guardRetire refuses any mutation that would retire an account still wired to
// a default slot. The caller repoints the slot first, then retires.
func guardRetire(ctx context.Context, q rowQuerier, accountID int, code, name string) error {
	slots, err := BlockingSlots(ctx, q, accountID)
	if err != nil {
		return err
	}
	if len(slots) == 0 {
		return nil
	}
	return ConflictError{
		Msg: fmt.Sprintf("Account %s %s is in use as a default account (%s). "+
			"Point the default at another account first.",
			code, name, strings.Join(slots, ", ")),
		BlockingSlots: slots,
	}
}

// hasLiveChildren reports whether the account still has undeleted children,
// which blocks a delete.
func hasLiveChildren(ctx context.Context, q rowQuerier, accountID int) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM coa_account
		WHERE parent_id = $1 AND coa_account_deleted_at IS NULL`, accountID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("count live children: %w", err)
	}
	return n > 0, nil
}
