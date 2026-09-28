package crmstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/duplicate"
)

// customerTypeCode is the record-type code of the customer stage. The `customer`
// table also holds leads and prospects, and only the customer stage has a name
// that must be unique — two leads at the same company are normal, and a
// prospect legitimately shares its name with the customer it later becomes.
const customerTypeCode = "CUST"

// checkCustomerNameFree returns a *duplicate.Error when another live
// customer-stage record — in any status, so Draft, Inactive and Credit Hold
// ones count — already carries `name`. typeID is the CUST record type;
// excludeUUID skips the record being renamed ("" on create or convert). A blank
// name is left to CreateRecord's own required-field check.
func (s *relationalStore) checkCustomerNameFree(ctx context.Context, pool *pgxpool.Pool, typeID int, name, excludeUUID string) error {
	if duplicate.Key(name) == "" {
		return nil
	}
	d := duplicate.Error{Entity: duplicate.EntityCustomer, Name: name}
	err := pool.QueryRow(ctx, `
		SELECT c.customer_uuid::text, c.customer_name, COALESCE(cs.crm_status_name, '')
		FROM customer c
		LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status
		WHERE c.record_type = $1 AND c.customer_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL("c.customer_name")+` = `+duplicate.NormalizeSQL("$2::text")+`
		  AND ($3::text = '' OR c.customer_uuid::text <> $3::text)
		ORDER BY c.customer_id
		LIMIT 1`, typeID, name, excludeUUID,
	).Scan(&d.ExistingID, &d.ExistingName, &d.ExistingStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check duplicate customer name: %w", err)
	}
	return &d
}
