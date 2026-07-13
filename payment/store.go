package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a payment id matches no live row.
var ErrNotFound = errors.New("payment not found")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

const headerSelect = `
	SELECT p.payment_uuid, p.payment_number,
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       c.customer_uuid, COALESCE(c.customer_name,''),
	       COALESCE(ou.id::text,''), p.payment_owner_id,
	       p.payment_method, COALESCE(pm.payment_method_name,''),
	       p.payment_reference_number, p.payment_date, p.payment_currency,
	       p.payment_memo, p.payment_internal_notes,
	       p.payment_amount, p.payment_applied_total, p.payment_unapplied_amount,
	       p.payment_custom_fields, p.payment_created_at, p.payment_updated_at, p.payment_record_version,
	       p.payment_id, p.payment_status, p.payment_customer_id
	FROM payment p
	JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
	JOIN customer c ON c.customer_id = p.payment_customer_id
	JOIN lkp_payment_method pm ON pm.payment_method_id = p.payment_method
	LEFT JOIN employee oe ON oe.employee_id = p.payment_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

type paymentMeta struct {
	internalID int
	statusID   int
	customerID int
}

func scanPayment(row pgx.Row) (*Payment, paymentMeta, error) {
	var (
		p          Payment
		ownerEmpID *int
		currencyID *int
		custom     map[string]any
		meta       paymentMeta
	)
	err := row.Scan(
		&p.ID, &p.Number,
		&p.StatusCode, &p.StatusName,
		&p.Customer.ID, &p.Customer.Name,
		&p.OwnerUserID, &ownerEmpID,
		&p.MethodID, &p.MethodName,
		&p.ReferenceNumber, &p.PaymentDate, &currencyID,
		&p.Memo, &p.InternalNotes,
		&p.Amount, &p.AppliedTotal, &p.UnappliedAmount,
		&custom, &p.CreatedAt, &p.UpdatedAt, &p.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.customerID,
	)
	if err != nil {
		return nil, paymentMeta{}, err
	}
	p.OwnerEmployeeID = ownerEmpID
	p.CurrencyID = currencyID
	if custom == nil {
		custom = map[string]any{}
	}
	p.CustomFields = custom
	p.Applications = []Application{}
	return &p, meta, nil
}

const applicationSelect = `
	SELECT pa.application_uuid, i.invoice_uuid, COALESCE(i.invoice_number,''),
	       pa.application_amount, pa.application_created_at
	FROM payment_application pa
	JOIN invoice i ON i.invoice_id = pa.invoice_id
	WHERE pa.payment_id = $1 AND pa.application_deleted_at IS NULL
	ORDER BY pa.application_created_at ASC`

func loadApplications(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Application, error) {
	rows, err := pool.Query(ctx, applicationSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load payment applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.InvoiceID, &a.InvoiceNumber, &a.Amount, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan payment application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get loads a single live payment (header + applications) by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Payment, error) {
	p, meta, err := scanPayment(pool.QueryRow(ctx, headerSelect+`
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get payment: %w", err)
	}
	apps, err := loadApplications(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	p.Applications = apps
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

// validateCustom validates in.CustomFields against the "payment" workflow's
// field definitions, if one has been seeded. No-ops when it hasn't (mirrors
// invoice.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "payment")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load payment workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load payment field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
