package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rowQuerier is the subset of pgx shared by *pgxpool.Pool and pgx.Tx, so store
// helpers work identically inside and outside a transaction. Callers inside a
// transaction pass the tx, which is what keeps a mutation and its audit row in
// the same transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// accountColumns is the shared projection. Every read path selects exactly
// these, in this order, so scanAccount is the single scanner.
const accountColumns = `
	a.coa_account_uuid, a.coa_account_code, a.coa_account_name, a.coa_account_description,
	a.subcategory_id, s.subcategory_code, s.subcategory_name, c.category_code, c.category_name,
	p.coa_account_uuid, a.coa_account_depth, a.coa_account_bs_pnl, a.coa_account_type,
	a.coa_account_attributes, a.coa_account_is_postable, a.coa_account_is_active,
	a.coa_account_is_visible, a.coa_account_is_system, a.coa_account_record_version,
	a.coa_account_created_at, a.coa_account_updated_at`

// accountFrom is the shared FROM/JOIN chain. The parent join is LEFT so
// top-level accounts (parent_id NULL) still return a row.
const accountFrom = `
	FROM coa_account a
	JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
	JOIN lkp_coa_category    c ON c.category_id    = s.category_id
	LEFT JOIN coa_account    p ON p.coa_account_id = a.parent_id`

// accountSelect is the full read projection over live rows.
const accountSelect = `SELECT ` + accountColumns + accountFrom

// liveOnly is the soft-delete predicate every read path ANDs in.
const liveOnly = ` a.coa_account_deleted_at IS NULL `

// scanAttributes is the JSONB destination type for coa_account_attributes.
type scanAttributes = map[string]any

// scanAccount reads one row of accountColumns. Attributes are masked here, at
// the single point every read passes through, so encrypted material cannot
// escape the store layer by way of a path that forgot to mask (AD-10).
func scanAccount(row pgx.Row) (*Account, error) {
	var (
		a          Account
		parentUUID *string
		attrs      scanAttributes
	)
	if err := row.Scan(
		&a.ID, &a.Code, &a.Name, &a.Description,
		&a.SubCategoryID, &a.SubCategoryCode, &a.SubCategoryName, &a.CategoryCode, &a.CategoryName,
		&parentUUID, &a.Depth, &a.BSPNL, &a.Type,
		&attrs, &a.IsPostable, &a.IsActive,
		&a.IsVisible, &a.IsSystem, &a.RecordVersion,
		&a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	a.ParentID = parentUUID
	a.Attributes = MaskAttributes(attrs)
	return &a, nil
}

// uuidPattern matches the canonical 8-4-4-4-12 hex form. google/uuid is not a
// dependency of this module, so validation is hand-rolled rather than adding
// one for a single format check.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validAccountUUID reports whether s is a syntactically valid UUID. Callers
// check this before querying so a malformed client string becomes a 400
// ClientError instead of a 22P02 the controller can only render as 500.
func validAccountUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

// nullableInt converts a non-positive employee id to SQL NULL, matching the
// convention in crmstore and inventory (employee id 0/unresolved => NULL).
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// accountIDByUUID resolves a public uuid to the internal serial id, returning
// ErrNotFound when the uuid matches nothing live.
func accountIDByUUID(ctx context.Context, q rowQuerier, uuid string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT coa_account_id FROM coa_account
		 WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve account uuid: %w", err)
	}
	return id, nil
}

// takenCodes returns every live account code, for the numbering allocator.
// The whole set is read rather than a filtered slice because both allocators
// need to see child codes and top-level codes together to reuse gaps
// correctly, and 127 seeded rows plus user additions is a trivially small set.
func takenCodes(ctx context.Context, q rowQuerier) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT coa_account_code FROM coa_account WHERE coa_account_deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list taken codes: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("scan taken code: %w", err)
		}
		out = append(out, code)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate taken codes: %w", err)
	}
	return out, nil
}

// ensurePool is a compile-time assertion that *pgxpool.Pool satisfies rowQuerier.
var _ rowQuerier = (*pgxpool.Pool)(nil)
