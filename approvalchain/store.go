package approvalchain

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrUnknownApprover is returned when a submitted approver employee id does
// not resolve to an active (non-deleted) employee in this tenant.
var ErrUnknownApprover = errors.New("one or more approver employee ids do not match an active employee")

// GateApprovers is one configured gate plus its currently active approvers,
// shaped for the GET .../approval-chain response.
type GateApprovers struct {
	StatusCode          string   `json:"statusCode"`
	StatusLabel         string   `json:"statusLabel"`
	ApproverEmployeeIDs []string `json:"approverEmployeeIds"`
}

// EligibleEmployee is an active employee who can be picked as an approver,
// shaped for the GET .../approval-chain response's employees list.
type EligibleEmployee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// EligibleEmployees lists every active employee in the tenant for the
// approval-chain approver picker. Callers reach this only after already
// passing the handler's workflow_config:read/configure check -- unlike
// /tenant/crm/lookups' employees field (gated separately behind user:read,
// for its broader staff-directory use on CRM forms), configuring who
// approves a workflow gate is itself the permission that should unlock
// seeing who can be picked, so this does not re-check ResourceUser.
func EligibleEmployees(ctx context.Context, pool *pgxpool.Pool) ([]EligibleEmployee, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.employee_id, COALESCE(NULLIF(u.full_name,''), u.email)
		FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE e.employee_deleted_at IS NULL AND u.status = 'active'
		ORDER BY COALESCE(NULLIF(u.full_name,''), u.email)`)
	if err != nil {
		return nil, fmt.Errorf("list eligible employees: %w", err)
	}
	defer rows.Close()
	out := []EligibleEmployee{}
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan eligible employee: %w", err)
		}
		out = append(out, EligibleEmployee{ID: strconv.Itoa(id), Name: name})
	}
	return out, rows.Err()
}

// GatesWithApprovers loads every configured gate for cfg, each with its
// currently active approver employee ids, in registry order.
func GatesWithApprovers(ctx context.Context, pool *pgxpool.Pool, cfg ModuleConfig) ([]GateApprovers, error) {
	recordTypeID, err := recordTypeIDByCode(ctx, pool, cfg.RecordTypeCode)
	if err != nil {
		return nil, err
	}
	out := make([]GateApprovers, 0, len(cfg.Gates))
	for _, gate := range cfg.Gates {
		statusID, label, err := statusIDAndLabelByCode(ctx, pool, recordTypeID, gate.StatusCode)
		if err != nil {
			return nil, err
		}
		ids, err := approverEmployeeIDs(ctx, pool, cfg.ApproverTable, recordTypeID, statusID)
		if err != nil {
			return nil, err
		}
		out = append(out, GateApprovers{StatusCode: gate.StatusCode, StatusLabel: label, ApproverEmployeeIDs: ids})
	}
	return out, nil
}

// ReplaceApprovers sets the active approver set for cfg's gate at statusCode
// to exactly employeeIDs (validated to be real, active employees in this
// tenant), replacing whatever was there. No count cap -- the 2-approver UI
// limit is enforced client-side only, matching every other approver endpoint
// in this codebase (see controllers/workflow.go SetWorkflowApprovers).
// createdBy may be 0 (unresolved caller), stored as NULL. Returns the
// employeeIDs that were set, for the caller to echo back.
func ReplaceApprovers(ctx context.Context, pool *pgxpool.Pool, cfg ModuleConfig, statusCode string, employeeIDs []string, createdBy int) ([]string, error) {
	recordTypeID, err := recordTypeIDByCode(ctx, pool, cfg.RecordTypeCode)
	if err != nil {
		return nil, err
	}
	statusID, _, err := statusIDAndLabelByCode(ctx, pool, recordTypeID, statusCode)
	if err != nil {
		return nil, err
	}

	empIDs := make([]int, 0, len(employeeIDs))
	for _, s := range employeeIDs {
		id, convErr := strconv.Atoi(s)
		if convErr != nil {
			return nil, ErrUnknownApprover
		}
		empIDs = append(empIDs, id)
	}
	unique := uniqueInts(empIDs)
	if len(unique) > 0 {
		var activeCount int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM employee WHERE employee_id = ANY($1) AND employee_deleted_at IS NULL`,
			unique).Scan(&activeCount); err != nil {
			return nil, fmt.Errorf("validate approver employees: %w", err)
		}
		if activeCount != len(unique) {
			return nil, ErrUnknownApprover
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin replace approvers: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE record_type_id = $1 AND record_status_id = $2`, cfg.ApproverTable),
		recordTypeID, statusID); err != nil {
		return nil, fmt.Errorf("clear existing approvers: %w", err)
	}
	for _, empID := range unique {
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s (record_type_id, record_status_id, approver_employee_id, created_by) VALUES ($1, $2, $3, $4)`, cfg.ApproverTable),
			recordTypeID, statusID, empID, nullIntOrNil(createdBy)); err != nil {
			return nil, fmt.Errorf("insert approver: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit replace approvers: %w", err)
	}
	setEmployeeIDs := make([]string, 0, len(unique))
	for _, empID := range unique {
		setEmployeeIDs = append(setEmployeeIDs, strconv.Itoa(empID))
	}
	return setEmployeeIDs, nil
}

func approverEmployeeIDs(ctx context.Context, pool *pgxpool.Pool, table string, recordTypeID, statusID int) ([]string, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(
		`SELECT approver_employee_id::text FROM %s WHERE record_type_id = $1 AND record_status_id = $2 AND is_active ORDER BY created_at`, table),
		recordTypeID, statusID)
	if err != nil {
		return nil, fmt.Errorf("list approvers: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan approver: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

func statusIDAndLabelByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, string, error) {
	var id int
	var name string
	err := q.QueryRow(ctx, `
		SELECT record_status_id, record_status_name FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id, &name)
	if err != nil {
		return 0, "", fmt.Errorf("status %q: %w", code, err)
	}
	return id, name, nil
}

func nullIntOrNil(id int) any {
	if id <= 0 {
		return nil
	}
	return id
}

func uniqueInts(xs []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
