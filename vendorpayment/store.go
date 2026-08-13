package vendorpayment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a vendor payment id matches no live row.
var ErrNotFound = errors.New("vendor payment not found")

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
	SELECT vp.vendor_payment_uuid, COALESCE(vp.vendor_payment_number,''),
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       v.vendor_uuid, COALESCE(NULLIF(vp.vendor_payment_vendor_name,''),
	           CASE WHEN v.vendor_type = 'Organization' THEN v.vendor_legal_name
	                ELSE TRIM(v.vendor_given_name || ' ' || v.vendor_family_name) END),
	       COALESCE(ou.id::text,''), vp.vendor_payment_owner_id,
	       vp.vendor_payment_method, COALESCE(pm.payment_method_name,''),
	       vp.vendor_payment_reference_number, vp.vendor_payment_date, vp.vendor_payment_scheduled_date, vp.vendor_payment_currency,
	       vp.vendor_payment_memo, vp.vendor_payment_internal_notes,
	       vp.vendor_payment_amount, vp.vendor_payment_applied_total, vp.vendor_payment_unapplied_amount,
	       vp.vendor_payment_approval_status, vp.vendor_payment_approved_by,
	       vp.vendor_payment_custom_fields, vp.vendor_payment_created_at, vp.vendor_payment_updated_at, vp.vendor_payment_record_version,
	       vp.vendor_payment_id, vp.vendor_payment_status, vp.vendor_payment_vendor_id
	FROM vendor_payment vp
	JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
	JOIN vendor v ON v.vendor_id = vp.vendor_payment_vendor_id
	JOIN lkp_payment_method pm ON pm.payment_method_id = vp.vendor_payment_method
	LEFT JOIN employee oe ON oe.employee_id = vp.vendor_payment_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

// paymentMeta carries internal (non-exported) ids alongside a scanned
// VendorPayment — used for keyset-cursor minting and never serialized.
type paymentMeta struct {
	internalID int
	statusID   int
	vendorID   int
}

func scanVendorPayment(row pgx.Row) (*VendorPayment, paymentMeta, error) {
	var (
		p             VendorPayment
		ownerEmpID    *int
		scheduledDate *time.Time
		currencyID    *int
		approvedByID  *int
		custom        map[string]any
		meta          paymentMeta
	)
	err := row.Scan(
		&p.ID, &p.Number,
		&p.StatusCode, &p.StatusName,
		&p.Vendor.ID, &p.Vendor.Name,
		&p.OwnerUserID, &ownerEmpID,
		&p.MethodID, &p.MethodName,
		&p.ReferenceNumber, &p.PaymentDate, &scheduledDate, &currencyID,
		&p.Memo, &p.InternalNotes,
		&p.Amount, &p.AppliedTotal, &p.UnappliedAmount,
		&p.ApprovalStatus, &approvedByID,
		&custom, &p.CreatedAt, &p.UpdatedAt, &p.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.vendorID,
	)
	if err != nil {
		return nil, paymentMeta{}, err
	}
	p.OwnerEmployeeID = ownerEmpID
	p.ScheduledDate = scheduledDate
	p.CurrencyID = currencyID
	p.ApprovedByEmployeeID = approvedByID
	if custom == nil {
		custom = map[string]any{}
	}
	p.CustomFields = custom
	p.Applications = []Application{}
	p.Refunds = []Refund{}
	return &p, meta, nil
}

const applicationSelect = `
	SELECT vpa.application_uuid, vb.vendor_bill_uuid, COALESCE(vb.vendor_bill_number,''),
	       vpa.application_amount, vpa.application_created_at
	FROM vendor_payment_application vpa
	JOIN vendor_bill vb ON vb.vendor_bill_id = vpa.vendor_bill_id
	WHERE vpa.vendor_payment_id = $1 AND vpa.application_deleted_at IS NULL
	ORDER BY vpa.application_created_at ASC`

func loadApplications(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Application, error) {
	rows, err := pool.Query(ctx, applicationSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor payment applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.VendorBillID, &a.VendorBillNumber, &a.Amount, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor payment application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const refundSelect = `
	SELECT vpr.refund_uuid, vb.vendor_bill_uuid, COALESCE(vb.vendor_bill_number,''),
	       vpr.refund_amount, vpr.refund_reason, vpr.refund_reference_number, vpr.refund_memo,
	       vpr.refund_refunded_at, vpr.refund_created_at
	FROM vendor_payment_refund vpr
	JOIN vendor_bill vb ON vb.vendor_bill_id = vpr.vendor_bill_id
	WHERE vpr.vendor_payment_id = $1 AND vpr.refund_deleted_at IS NULL
	ORDER BY vpr.refund_created_at ASC`

func loadRefunds(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Refund, error) {
	rows, err := pool.Query(ctx, refundSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor payment refunds: %w", err)
	}
	defer rows.Close()
	out := []Refund{}
	for rows.Next() {
		var r Refund
		if err := rows.Scan(&r.ID, &r.VendorBillID, &r.VendorBillNumber, &r.Amount, &r.Reason, &r.ReferenceNumber, &r.Memo, &r.RefundedAt, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor payment refund: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get loads a single live vendor payment (header + applications + refunds) by
// its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*VendorPayment, error) {
	p, meta, err := scanVendorPayment(pool.QueryRow(ctx, headerSelect+`
		WHERE vp.vendor_payment_uuid = $1 AND vp.vendor_payment_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vendor payment: %w", err)
	}
	apps, err := loadApplications(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	p.Applications = apps
	refunds, err := loadRefunds(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	p.Refunds = refunds
	return p, nil
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

// validateCustom validates in.CustomFields against the "vendor_payment"
// workflow's field definitions, if one has been seeded. No-ops when it
// hasn't (mirrors payment.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "vendor_payment")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load vendor_payment workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load vendor_payment field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
