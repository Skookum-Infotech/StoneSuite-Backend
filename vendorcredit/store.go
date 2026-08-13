// vendorcredit/store.go — shared helpers used by every verb file, plus Get.
package vendorcredit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a vendor credit uuid matches no live row.
var ErrNotFound = errors.New("vendor credit not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// vcrdRecordTypeCode is the lkp_record_type code for Vendor Credit.
const vcrdRecordTypeCode = "VCRD"

// draftStatusCode is the status every new vendor credit starts at.
const draftStatusCode = "DRFT"

// activeVendorStatusCode is the lkp_record_status code a vendor must carry
// (AD-8) for a vendor credit to be created against it.
const activeVendorStatusCode = "ACT_"

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
// Use this — never nullableInt — for any *_deleted_by column paired with a
// NOT NULL *_deleted_at via a CHECK constraint.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) -- an invalid caller-supplied reference id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
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

// round2 rounds x to two decimal places (money precision).
func round2(x float64) float64 { return math.Round(x*100) / 100 }

// activeVendorSnapshot loads a vendor's internal id and display name for the
// create-time snapshot (AD-12), additionally requiring the vendor be active
// (AD-8) -- stricter than vendorbill.vendorSnapshot, which only checks
// vendor_deleted_at IS NULL. The not-found and inactive cases are resolved
// from a single row read (deleted/missing vendors never resolve a row at
// all, matching vendorSnapshot's existing "Unknown vendor." behavior) and
// then a separate status check, so the two failure modes surface distinct,
// correct messages instead of collapsing behind one WHERE clause.
func activeVendorSnapshot(ctx context.Context, q workflow.Querier, vendorUUID string) (id int, name string, err error) {
	var statusCode string
	err = q.QueryRow(ctx, `
		SELECT v.vendor_id,
		       CASE WHEN v.vendor_type = 'Organization' THEN v.vendor_legal_name
		            ELSE TRIM(v.vendor_given_name || ' ' || v.vendor_family_name) END,
		       COALESCE(rs.record_status_code, '')
		FROM vendor v
		LEFT JOIN lkp_record_status rs ON rs.record_status_id = v.vendor_status
		WHERE v.vendor_uuid = $1 AND v.vendor_deleted_at IS NULL`, vendorUUID).Scan(&id, &name, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ClientError{Msg: "Unknown vendor."}
	}
	if err != nil {
		return 0, "", fmt.Errorf("load vendor snapshot: %w", err)
	}
	if statusCode != activeVendorStatusCode {
		return 0, "", ClientError{Msg: "Vendor is not active."}
	}
	return id, name, nil
}

// validateCustom validates custom fields against the "vendor_credit"
// workflow's field definitions, if one has been seeded. No-ops when it
// hasn't (mirrors vendorbill.validateCustom). The pre-existing v1
// vendor_credit workflow supplies these definitions; its states are unused
// by this module (AD-1).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "vendor_credit")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load vendor_credit workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load vendor_credit field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// internalIDByUUID resolves a vendor credit's external uuid to its internal
// id and current status code, or ErrNotFound.
func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, string, error) {
	var internalID int
	var statusCode string
	err := pool.QueryRow(ctx, `
		SELECT vc.vendor_credit_id, rs.record_status_code
		FROM vendor_credit vc
		JOIN lkp_record_status rs ON rs.record_status_id = vc.vendor_credit_status
		WHERE vc.vendor_credit_uuid = $1 AND vc.vendor_credit_deleted_at IS NULL`, id,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve vendor credit: %w", err)
	}
	return internalID, statusCode, nil
}
