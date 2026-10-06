package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CurrenciesConflict reports whether two document currencies are both set and
// differ. A NULL currency (no default-currency helper exists in the backend)
// is treated as "unspecified" and matches anything. No conversion is ever done.
func CurrenciesConflict(a, b *int) bool {
	return a != nil && b != nil && *a != *b
}

// currencyLabel resolves a currency id to its ISO code, falling back to the id.
func currencyLabel(ctx context.Context, tx pgx.Tx, id int) (string, error) {
	var code string
	err := tx.QueryRow(ctx, `SELECT currency_code FROM lkp_currency WHERE currency_id = $1`, id).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Sprintf("#%d", id), nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve currency code: %w", err)
	}
	return code, nil
}

// CurrencyConflictMsg returns "" when src and dst currencies are compatible,
// otherwise a user-facing message such as "Currency mismatch: payment is in
// USD but invoice is in EUR." Callers wrap a non-empty result in their own
// module ClientError (400).
func CurrencyConflictMsg(ctx context.Context, tx pgx.Tx, srcLabel string, src *int, dstLabel string, dst *int) (string, error) {
	if !CurrenciesConflict(src, dst) {
		return "", nil
	}
	sc, err := currencyLabel(ctx, tx, *src)
	if err != nil {
		return "", err
	}
	dc, err := currencyLabel(ctx, tx, *dst)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Currency mismatch: %s is in %s but %s is in %s.", srcLabel, sc, dstLabel, dc), nil
}
