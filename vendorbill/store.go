package vendorbill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a vendor bill id matches no live row.
var ErrNotFound = errors.New("vendor bill not found")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// systemEmployeeID is the fallback actor for soft-delete columns that must
// never be NULL when their paired *_deleted_at timestamp is set (enforced by
// a CHECK constraint) — used when the caller has no resolvable employee id.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID if it's unset
// (0). Use this — never nullableInt — for any *_deleted_by column paired
// with a NOT NULL *_deleted_at via a CHECK constraint.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

const headerSelect = `
	SELECT vb.vendor_bill_uuid, COALESCE(vb.vendor_bill_number,''),
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       v.vendor_uuid, vb.vendor_bill_vendor_name,
	       COALESCE(ou.id::text,''), vb.vendor_bill_owner_id,
	       vb.vendor_bill_reference_number, vb.vendor_bill_date, vb.vendor_bill_due_date,
	       vb.vendor_bill_memo, vb.vendor_bill_internal_notes,
	       vb.vendor_bill_grand_total, vb.vendor_bill_amount_paid, vb.vendor_bill_balance_due,
	       vb.vendor_bill_custom_fields, vb.vendor_bill_created_at, vb.vendor_bill_updated_at, vb.vendor_bill_record_version,
	       vb.vendor_bill_id, vb.vendor_bill_status, vb.vendor_bill_vendor_id
	FROM vendor_bill vb
	JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
	JOIN vendor v ON v.vendor_id = vb.vendor_bill_vendor_id
	LEFT JOIN employee oe ON oe.employee_id = vb.vendor_bill_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

type vbMeta struct {
	internalID int
	statusID   int
	vendorID   int
}

func scanVendorBill(row pgx.Row) (*VendorBill, vbMeta, error) {
	var (
		vb         VendorBill
		ownerEmpID *int
		dueDate    *time.Time
		custom     map[string]any
		meta       vbMeta
	)
	err := row.Scan(
		&vb.ID, &vb.Number,
		&vb.StatusCode, &vb.StatusName,
		&vb.Vendor.ID, &vb.Vendor.Name,
		&vb.OwnerUserID, &ownerEmpID,
		&vb.ReferenceNumber, &vb.BillDate, &dueDate,
		&vb.Memo, &vb.InternalNotes,
		&vb.GrandTotal, &vb.AmountPaid, &vb.BalanceDue,
		&custom, &vb.CreatedAt, &vb.UpdatedAt, &vb.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.vendorID,
	)
	if err != nil {
		return nil, vbMeta{}, err
	}
	vb.OwnerEmployeeID = ownerEmpID
	vb.DueDate = dueDate
	if custom == nil {
		custom = map[string]any{}
	}
	vb.CustomFields = custom
	return &vb, meta, nil
}

// Get loads a single live vendor bill by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*VendorBill, error) {
	vb, _, err := scanVendorBill(pool.QueryRow(ctx, headerSelect+`
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vendor bill: %w", err)
	}
	return vb, nil
}

func typeIDByCode(ctx context.Context, pool *pgxpool.Pool, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

func statusIDByCode(ctx context.Context, pool *pgxpool.Pool, typeID int, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// validateCustom validates in.CustomFields against the "vendor_bill"
// workflow's field definitions, if one has been seeded. No-ops when it
// hasn't (mirrors payment.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "vendor_bill")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load vendor_bill workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load vendor_bill field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
