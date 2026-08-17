package expense

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an expense uuid matches nothing live.
var ErrNotFound = errors.New("expense not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring requisition.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// expenseRecordTypeCode is the lkp_record_type code for Expense.
const expenseRecordTypeCode = "EXPN"

// draftStatusCode is the status every new expense claim starts at.
const draftStatusCode = "DRFT"

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// systemEmployeeID is the fallback actor for soft-delete columns that must
// never be NULL when their paired *_deleted_at timestamp is set (enforced by
// a CHECK constraint) — used when the caller has no resolvable employee id.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID if it's unset (0).
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

// nullableDate returns the given "yyyy-mm-dd" string as a nullable date arg.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix, e.g. "::date") so an INSERT/UPDATE's column list and argument list
// are always built from the same slice.
type colVal struct {
	col  string
	val  any
	cast string
}

// buildInsert renders an INSERT ... VALUES (...) RETURNING statement from
// column/value pairs, numbering placeholders by position.
func buildInsert(table string, cv []colVal, returning string) (string, []any) {
	cols := make([]string, len(cv))
	phs := make([]string, len(cv))
	args := make([]any, len(cv))
	for i, c := range cv {
		cols[i] = c.col
		args[i] = c.val
		phs[i] = fmt.Sprintf("$%d%s", i+1, c.cast)
	}
	sql := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// buildUpdateSet renders an "UPDATE ... SET col=$n, ... WHERE <where>"
// statement. leadingArgs are bound first; cv's placeholders continue after.
func buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any) {
	sets := make([]string, 0, len(cv)+len(extraSets))
	args := make([]any, 0, len(leadingArgs)+len(cv))
	args = append(args, leadingArgs...)
	for _, c := range cv {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", c.col, len(args), c.cast))
	}
	sets = append(sets, extraSets...)
	sql := "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	return sql, args
}

// recordTypeIDByCode resolves a lkp_record_type code to its internal id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code (scoped to a record type)
// to its internal id.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// categorySnapshot is what a line needs from its category at add time.
type categorySnapshot struct {
	id   int
	name string
	code string
}

// categoryByCode resolves an expense category by its code. Returns
// ClientError when the code does not resolve to a live, active category.
func categoryByCode(ctx context.Context, q workflow.Querier, code string) (*categorySnapshot, error) {
	var s categorySnapshot
	err := q.QueryRow(ctx, `
		SELECT expense_category_id, expense_category_name, expense_category_code
		FROM lkp_expense_category
		WHERE expense_category_code = $1 AND expense_category_is_active AND expense_category_deleted_at IS NULL`,
		code).Scan(&s.id, &s.name, &s.code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown expense category: " + code}
	}
	if err != nil {
		return nil, fmt.Errorf("load expense category: %w", err)
	}
	return &s, nil
}

// employeeActive reports whether an employee is active and not soft-deleted
// — the claimant-side layer of this module's inactive-user enforcement
// (spec AD-8, layer 2). Local to this package; does not touch the shared
// resolveEmployeeID helper (controllers/crm_admin.go), which ~40 other files
// depend on unchanged.
func employeeActive(ctx context.Context, q workflow.Querier, employeeID int) (bool, error) {
	var active bool
	err := q.QueryRow(ctx, `
		SELECT employee_is_active FROM employee
		WHERE employee_id = $1 AND employee_deleted_at IS NULL`, employeeID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check employee active: %w", err)
	}
	return active, nil
}

// validateCustom validates custom fields against the pre-existing legacy v1
// JSONB "expense" workflow's field definitions (≤15, typed) — the corrected
// skeleton (mirrors requisition.validateCustom / purchaseorder.validateCustom).
// That legacy workflow row (schema.sql ~2127-2165) is deliberately reused
// here as the custom-field-definition host: a tenant admin configures custom
// fields for this relational module through the same admin UI that manages
// the legacy engine's workflow field definitions, without a parallel config
// system. This relational module's own JSONB column (expense_custom_fields)
// is what actually stores the values; the legacy workflow only supplies the
// field *definitions* to validate against.
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "expense")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load expense workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load expense field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
