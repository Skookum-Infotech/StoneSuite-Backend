package invoice

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/portal"
	"stonesuite-backend/query"
)

// The customer-portal read path.
//
// This is deliberately NOT the RBAC scope path. Scope (all|own) narrows by
// the employee who owns a record, which is meaningless for an external customer, and it is
// resolved from role grants where "broadest scope wins" across roles — one
// stray grant of scope=all would widen a portal caller to the whole tenant.
//
// Instead the customer is a REQUIRED PARAMETER of these functions. It is
// resolved from the session (portal.ResolveSession) and can never be supplied,
// overridden, or widened by a request: a caller-supplied customer_id filter is
// ANDed on top by query.Build and can only narrow further. That is what makes
// this a security boundary rather than a convenience filter.

// portalWhere builds the leading predicate every portal query starts from:
// live rows, this customer only, and only lifecycle states a customer may see.
// It returns the clauses, their args, and the next free placeholder index.
func portalWhere(customerID int) ([]string, []any, int, error) {
	vis, ok := portal.Visible(portal.ModuleInvoice)
	if !ok {
		// Unreachable with a constant module, but fail closed rather than
		// silently dropping the status filter if that ever changes.
		return nil, nil, 0, errors.New("portal visibility not configured for invoice")
	}
	where := []string{
		"i.invoice_deleted_at IS NULL",
		"i.invoice_customer_id = $1",
		// rs is the module's own status join, so the code is unambiguous
		// without also constraining record type.
		"rs.record_status_code = ANY($2)",
	}
	return where, []any{customerID, vis.StatusCodes}, 3, nil
}

// PortalSearch lists the documents belonging to one customer, with the same
// filter/sort/keyset pagination the internal search offers.
//
// Returns headers only, matching Search.
func PortalSearch(ctx context.Context, pool *pgxpool.Pool, customerID int, req query.Request) (Page, error) {
	where, args, nextIdx, err := portalWhere(customerID)
	if err != nil {
		return Page{}, err
	}

	built, err := query.Build(req, resolver{}, nextIdx)
	if err != nil {
		return Page{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	q := headerSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("portal search invoice: %w", err)
	}
	defer rows.Close()

	out := []Invoice{}
	metas := []invoiceMeta{}
	for rows.Next() {
		rec, meta, err := scanInvoice(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan invoice: %w", err)
		}
		out = append(out, *rec)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("portal search invoice: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		page.Records = out[:built.EffLimit]
		lastIdx := built.EffLimit - 1
		last, lastMeta := page.Records[lastIdx], metas[lastIdx]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, lastMeta, built.Sort.Field))
	}
	return page, nil
}

// PortalGet loads one document for a customer, with its full body.
//
// Returns ErrNotFound — never a permission error — when the document belongs to
// another customer or is in a state the portal does not show. A 404 rather than
// a 403 means ids cannot be probed for existence, matching the convention the
// internal single-record handlers already follow.
func PortalGet(ctx context.Context, pool *pgxpool.Pool, customerID int, uuid string) (*Invoice, error) {
	where, args, nextIdx, err := portalWhere(customerID)
	if err != nil {
		return nil, err
	}
	where = append(where, fmt.Sprintf("i.invoice_uuid = $%d", nextIdx))
	args = append(args, uuid)

	q := headerSelect + " WHERE " + strings.Join(where, " AND ")

	rec, meta, err := scanInvoice(pool.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("portal get invoice: %w", err)
	}
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	rec.Items = lines

	return rec, nil
}
