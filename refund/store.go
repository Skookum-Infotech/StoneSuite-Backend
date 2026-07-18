package refund

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a refund id matches no live row.
var ErrNotFound = errors.New("refund not found")

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
	SELECT rfnd.refund_uuid, rfnd.refund_number,
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       c.customer_uuid, COALESCE(c.customer_name,''),
	       COALESCE(ou.id::text,''), rfnd.refund_owner_id,
	       COALESCE(pay.payment_uuid::text,''), COALESCE(cm.credit_memo_uuid::text,''), COALESCE(inv.invoice_uuid::text,''),
	       rfnd.refund_method, COALESCE(pm.payment_method_name,''),
	       rfnd.refund_reference_number, rfnd.refund_date, rfnd.refund_currency,
	       rfnd.refund_reason, rfnd.refund_memo, rfnd.refund_internal_notes,
	       rfnd.refund_amount, rfnd.refund_applied_total, rfnd.refund_unapplied_amount,
	       rfnd.refund_custom_fields, rfnd.refund_created_at, rfnd.refund_updated_at, rfnd.refund_record_version,
	       rfnd.refund_id, rfnd.refund_status, rfnd.refund_customer_id
	FROM refund rfnd
	JOIN lkp_record_status rs ON rs.record_status_id = rfnd.refund_status
	JOIN customer c ON c.customer_id = rfnd.refund_customer_id
	JOIN lkp_payment_method pm ON pm.payment_method_id = rfnd.refund_method
	LEFT JOIN employee oe ON oe.employee_id = rfnd.refund_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id
	LEFT JOIN payment pay ON pay.payment_id = rfnd.refund_payment_id
	LEFT JOIN credit_memo cm ON cm.credit_memo_id = rfnd.refund_credit_memo_id
	LEFT JOIN invoice inv ON inv.invoice_id = rfnd.refund_invoice_id`

type refundMeta struct {
	internalID int
	statusID   int
	customerID int
}

func scanRefund(row pgx.Row) (*Refund, refundMeta, error) {
	var (
		rf         Refund
		ownerEmpID *int
		currencyID *int
		custom     map[string]any
		meta       refundMeta
	)
	err := row.Scan(
		&rf.ID, &rf.Number,
		&rf.StatusCode, &rf.StatusName,
		&rf.Customer.ID, &rf.Customer.Name,
		&rf.OwnerUserID, &ownerEmpID,
		&rf.PaymentID, &rf.CreditMemoID, &rf.InvoiceID,
		&rf.MethodID, &rf.MethodName,
		&rf.ReferenceNumber, &rf.RefundDate, &currencyID,
		&rf.Reason, &rf.Memo, &rf.InternalNotes,
		&rf.Amount, &rf.AppliedTotal, &rf.UnappliedAmount,
		&custom, &rf.CreatedAt, &rf.UpdatedAt, &rf.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.customerID,
	)
	if err != nil {
		return nil, refundMeta{}, err
	}
	rf.OwnerEmployeeID = ownerEmpID
	rf.CurrencyID = currencyID
	if custom == nil {
		custom = map[string]any{}
	}
	rf.CustomFields = custom
	rf.Applications = []Application{}
	return &rf, meta, nil
}

const applicationSelect = `
	SELECT ra.application_uuid,
	       COALESCE(pay.payment_uuid::text,''), COALESCE(pay.payment_number,''),
	       COALESCE(cm.credit_memo_uuid::text,''), COALESCE(cm.credit_memo_number,''),
	       ra.application_amount, ra.application_created_at
	FROM refund_application ra
	LEFT JOIN payment pay ON pay.payment_id = ra.payment_id
	LEFT JOIN credit_memo cm ON cm.credit_memo_id = ra.credit_memo_id
	WHERE ra.refund_id = $1 AND ra.application_deleted_at IS NULL
	ORDER BY ra.application_created_at ASC`

func loadApplications(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Application, error) {
	rows, err := pool.Query(ctx, applicationSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load refund applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.PaymentID, &a.PaymentNumber, &a.CreditMemoID, &a.CreditMemoNumber, &a.Amount, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan refund application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get loads a single live refund (header + applications) by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Refund, error) {
	rf, meta, err := scanRefund(pool.QueryRow(ctx, headerSelect+`
		WHERE rfnd.refund_uuid = $1 AND rfnd.refund_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get refund: %w", err)
	}
	apps, err := loadApplications(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	rf.Applications = apps
	return rf, nil
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

// validateCustom validates in.CustomFields against the "refund" workflow's
// field definitions, if one has been seeded. No-ops when it hasn't (mirrors
// payment.validateCustom / invoice.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "refund")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load refund workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load refund field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
