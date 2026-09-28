package vendors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/workflow"
)

// vendorNameSQL is a vendor's name for duplicate purposes: a Person's given +
// family name, an Organization's legal name. A person's honorific and middle
// name are left out (displayName includes the honorific, but "Dr. Ada Lovelace"
// and "Ada Lovelace" are the same vendor), and the two types compare against
// each other — a person and a business with the same name are one duplicate to
// the user. Alias `v` is the vendor table.
const vendorNameSQL = `CASE WHEN v.vendor_type = 'Person'
	THEN concat_ws(' ', NULLIF(btrim(v.vendor_given_name), ''), NULLIF(btrim(v.vendor_family_name), ''))
	ELSE v.vendor_legal_name END`

// vendorDuplicateName is the Go mirror of vendorNameSQL for a create/update
// input, so a rename can be detected without a round trip.
func vendorDuplicateName(vendorType string, in vendorFields) string {
	if vendorType == "Person" {
		return strings.Join(strings.Fields(in.GivenName+" "+in.FamilyName), " ")
	}
	return in.LegalName
}

// checkVendorNameFree returns a *duplicate.Error when another live vendor —
// in any status, so Inactive and Blocked ones count — already has `name`.
// excludeUUID skips the vendor being renamed ("" on create). A blank name is
// left to validateVendorType.
func checkVendorNameFree(ctx context.Context, q workflow.Querier, name, excludeUUID string) error {
	if duplicate.Key(name) == "" {
		return nil
	}
	d := duplicate.Error{Entity: duplicate.EntityVendor, Name: name}
	err := q.QueryRow(ctx, `
		SELECT v.vendor_uuid::text, `+vendorNameSQL+`, rs.record_status_name
		FROM vendor v
		JOIN lkp_record_status rs ON rs.record_status_id = v.vendor_status
		WHERE v.vendor_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL(vendorNameSQL)+` = `+duplicate.NormalizeSQL("$1::text")+`
		  AND ($2::text = '' OR v.vendor_uuid::text <> $2::text)
		ORDER BY v.vendor_id
		LIMIT 1`, name, excludeUUID,
	).Scan(&d.ExistingID, &d.ExistingName, &d.ExistingStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check duplicate vendor name: %w", err)
	}
	return &d
}
