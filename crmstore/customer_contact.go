package crmstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CustomerDoNotContact reports whether the live customer customerUUID is
// flagged Do Not Contact (customer_do_not_contact). A missing or deleted
// customer reports false: there is no live preference to honor.
func CustomerDoNotContact(ctx context.Context, pool *pgxpool.Pool, customerUUID string) (bool, error) {
	var dnc bool
	err := pool.QueryRow(ctx, `
		SELECT customer_do_not_contact FROM customer
		WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID).Scan(&dnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load customer do-not-contact %s: %w", customerUUID, err)
	}
	return dnc, nil
}
