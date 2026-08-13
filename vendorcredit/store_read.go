// vendorcredit/store_read.go — the base SELECT, row-scan, and Get, split out
// of store.go to keep it under the repo's 300-line file cap.
package vendorcredit

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// vcSelect is the base SELECT shared by Get and Search. Column order must
// match scanVendorCredit's Scan(...) arg order exactly. Table alias `vc`
// matches resolver.go's field expressions.
const vcSelect = `
	SELECT vc.vendor_credit_uuid, COALESCE(vc.vendor_credit_number,''),
	       rs.record_status_code, rs.record_status_name,
	       v.vendor_uuid, vc.vendor_credit_vendor_name,
	       COALESCE(ou.id::text,''), vc.vendor_credit_owner_id,
	       vc.vendor_credit_reference_number, vc.vendor_credit_date, vc.vendor_credit_reason,
	       vc.vendor_credit_memo, vc.vendor_credit_internal_notes,
	       vc.vendor_credit_grand_total, vc.vendor_credit_applied_total, vc.vendor_credit_unapplied_amount,
	       vc.vendor_credit_custom_fields,
	       vc.vendor_credit_created_at, vc.vendor_credit_updated_at, vc.vendor_credit_record_version,
	       vc.vendor_credit_id, vc.vendor_credit_status, vc.vendor_credit_vendor_id
	FROM vendor_credit vc
	JOIN lkp_record_status rs ON rs.record_status_id = vc.vendor_credit_status
	JOIN vendor v ON v.vendor_id = vc.vendor_credit_vendor_id
	LEFT JOIN employee oe ON oe.employee_id = vc.vendor_credit_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

// vcMeta carries the internal numeric ids a vendor credit row has but the
// API response deliberately does not expose. Search needs them to mint a
// keyset cursor for sorts that run on those columns (status, vendor_id).
type vcMeta struct {
	internalID int
	statusID   int
	vendorID   int
}

func scanVendorCredit(row pgx.Row) (*VendorCredit, vcMeta, error) {
	var (
		vc         VendorCredit
		ownerEmpID *int
		custom     map[string]any
		meta       vcMeta
	)
	err := row.Scan(
		&vc.ID, &vc.Number,
		&vc.StatusCode, &vc.StatusName,
		&vc.Vendor.ID, &vc.Vendor.Name,
		&vc.OwnerUserID, &ownerEmpID,
		&vc.ReferenceNumber, &vc.CreditDate, &vc.Reason,
		&vc.Memo, &vc.InternalNotes,
		&vc.GrandTotal, &vc.AppliedTotal, &vc.UnappliedAmount,
		&custom,
		&vc.CreatedAt, &vc.UpdatedAt, &vc.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.vendorID,
	)
	if err != nil {
		return nil, vcMeta{}, err
	}
	vc.OwnerEmployeeID = ownerEmpID
	// The column is NOT NULL DEFAULT '{}', but a NULL can still arrive
	// through a LEFT JOIN or an older row; never hand a nil map back to
	// callers.
	if custom == nil {
		custom = map[string]any{}
	}
	vc.CustomFields = custom
	vc.Applications = []Application{}
	return &vc, meta, nil
}

// vcApplicationSelect loads live applications for one vendor credit, joined
// to the target vendor bill's display fields.
const vcApplicationSelect = `
	SELECT vca.application_uuid, vb.vendor_bill_uuid, COALESCE(vb.vendor_bill_number,''),
	       vca.application_amount, vca.application_created_at
	FROM vendor_credit_application vca
	JOIN vendor_bill vb ON vb.vendor_bill_id = vca.vendor_bill_id
	WHERE vca.vendor_credit_id = $1 AND vca.application_deleted_at IS NULL
	ORDER BY vca.application_created_at ASC`

func loadApplications(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Application, error) {
	rows, err := pool.Query(ctx, vcApplicationSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor credit applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.VendorBillID, &a.VendorBillNumber, &a.Amount, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor credit application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get loads a single live vendor credit (header + live applications) by its
// external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*VendorCredit, error) {
	vc, meta, err := scanVendorCredit(pool.QueryRow(ctx, vcSelect+`
		WHERE vc.vendor_credit_uuid = $1 AND vc.vendor_credit_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vendor credit: %w", err)
	}
	apps, err := loadApplications(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	vc.Applications = apps
	return vc, nil
}
