package purchaseorder

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// IncomingStatusCodes are the purchase order statuses counted as "sent to
// the vendor, not yet fully received" by the Purchases & requisitions
// status dashboard widget -- SENT (nothing received yet) and PART
// (partially received). DRFT/PAPV/APPV haven't been sent (nothing to
// receive yet); RCVD/CLSD/CANC have nothing outstanding left (see
// transitions.go's allowedTransitions).
var IncomingStatusCodes = []string{"SENT", "PART"}

// OpenTotals is DashboardOpen's aggregate half: how many SENT/PART purchase
// orders are outstanding and their combined not-yet-received value (each
// line's stored total prorated by its unreceived quantity fraction, so a
// 90%-received order contributes only its last 10%), split into the full
// incoming set and the subset that's also overdue (expected date already
// passed). A purchase order's approval_status is 'approved' by the time it
// reaches SENT (APPV is a prerequisite -- see transitions.go), so this set
// never overlaps DashboardPendingApproval's: a widget-level total across
// both never double-counts a single order.
type OpenTotals struct {
	IncomingCount int
	IncomingValue float64
	OverdueCount  int
	OverdueValue  float64
}

// OverduePO is one row in the widget's overdue-receipt worklist -- a
// SENT/PART order whose expected delivery date has passed, ranked most
// overdue first. Value is the same unreceived-fraction proration as
// OpenTotals, not the order's full grand total.
type OverduePO struct {
	ID           string
	RecordNumber string
	Vendor       string
	Value        float64
	DaysOverdue  int
}

// PendingApprovalPO is one purchase order awaiting approval sign-off,
// ranked oldest-first (longest waiting) by DashboardPendingApproval. Value
// is the order's full grand total -- nothing has shipped or been received
// yet at this stage, so there is no unreceived fraction to prorate.
type PendingApprovalPO struct {
	ID           string
	RecordNumber string
	Vendor       string
	Value        float64
	CreatedAt    time.Time
}

// PendingApprovalResult is DashboardPendingApproval's result. Eligible is
// false when the caller is not a configured approver for purchase orders at
// all (and not a super admin) -- distinct from "eligible but Rows is empty
// because nothing is pending" -- so the caller can render "not applicable
// to you" instead of a misleading zero (mirrors
// controllers.approvalModuleCountAndOldest's own eligible/count
// distinction). Rows holds up to the requested limit, oldest first;
// TotalCount/TotalValue are the true totals across every matching order
// (via COUNT/SUM OVER(), mirrors invoice.TopCustomersByRevenue's
// window-function total-alongside-limit pattern), not just the returned
// page.
type PendingApprovalResult struct {
	Eligible   bool
	Rows       []PendingApprovalPO
	TotalCount int
	TotalValue float64
}

// scopeWhere builds the owner-scope fragment shared by DashboardOpen's two
// queries, starting parameter numbering at startIdx.
func scopeWhere(ownerID *int, startIdx int) (clause string, args []any, nextIdx int) {
	nextIdx = startIdx
	if ownerID == nil {
		return "", nil, nextIdx
	}
	clause = fmt.Sprintf("po.purchase_order_owner_id = $%d", nextIdx)
	args = append(args, *ownerID)
	nextIdx++
	return clause, args, nextIdx
}

// DashboardOpen loads the Purchases & requisitions status widget's
// incoming/overdue aggregate and overdue-receipt worklist, scoped to the
// caller's RBAC scope. Returns a zero OpenTotals and nil rows (not an
// error) when scope is not "all" and the caller has no employee record --
// mirrors Search's own convention.
func DashboardOpen(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, overdueLimit int) (OpenTotals, []OverduePO, error) {
	var ownerID *int
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return OpenTotals{}, nil, nil
		}
		ownerID = &empID
	}

	totals, err := openTotals(ctx, pool, ownerID)
	if err != nil {
		return OpenTotals{}, nil, fmt.Errorf("purchase order open totals: %w", err)
	}
	overdue, err := overduePOs(ctx, pool, ownerID, overdueLimit)
	if err != nil {
		return OpenTotals{}, nil, fmt.Errorf("purchase order overdue list: %w", err)
	}
	return totals, overdue, nil
}

// outstandingLineExpr is the per-line "how much of this line is still owed"
// proration shared by openTotals and overduePOs: the unreceived quantity
// fraction (clamped at zero -- qty_received can exceed quantity for an
// over-delivery, see schema.sql's chk_poi_qty_received_nonneg comment)
// times the line's own stored total, which is already net of that line's
// discount and tax.
const outstandingLineExpr = `COALESCE(SUM(GREATEST(poi.quantity - poi.qty_received, 0) / NULLIF(poi.quantity, 0) * poi.line_total), 0)`

func openTotals(ctx context.Context, pool *pgxpool.Pool, ownerID *int) (OpenTotals, error) {
	where := []string{"po.purchase_order_deleted_at IS NULL", "rs.record_status_code = ANY($1)"}
	args := []any{IncomingStatusCodes}
	scopeClause, scopeArgs, _ := scopeWhere(ownerID, 2)
	if scopeClause != "" {
		where = append(where, scopeClause)
		args = append(args, scopeArgs...)
	}

	q := `
		SELECT COUNT(*), COALESCE(SUM(line_out.outstanding), 0),
		       COUNT(*) FILTER (WHERE po.purchase_order_expected_date < CURRENT_DATE),
		       COALESCE(SUM(line_out.outstanding) FILTER (WHERE po.purchase_order_expected_date < CURRENT_DATE), 0)
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		JOIN LATERAL (
			SELECT ` + outstandingLineExpr + ` AS outstanding
			FROM purchase_order_item poi
			WHERE poi.purchase_order_id = po.purchase_order_id AND poi.item_deleted_at IS NULL
		) line_out ON true
		WHERE ` + strings.Join(where, " AND ")

	var t OpenTotals
	if err := pool.QueryRow(ctx, q, args...).Scan(&t.IncomingCount, &t.IncomingValue, &t.OverdueCount, &t.OverdueValue); err != nil {
		return OpenTotals{}, fmt.Errorf("query open totals: %w", err)
	}
	return t, nil
}

func overduePOs(ctx context.Context, pool *pgxpool.Pool, ownerID *int, limit int) ([]OverduePO, error) {
	where := []string{
		"po.purchase_order_deleted_at IS NULL",
		"rs.record_status_code = ANY($1)",
		"po.purchase_order_expected_date < CURRENT_DATE",
	}
	args := []any{IncomingStatusCodes}
	scopeClause, scopeArgs, _ := scopeWhere(ownerID, 2)
	if scopeClause != "" {
		where = append(where, scopeClause)
		args = append(args, scopeArgs...)
	}

	q := `
		SELECT po.purchase_order_uuid, COALESCE(po.purchase_order_number, ''), po.purchase_order_vendor_name,
		       line_out.outstanding, (CURRENT_DATE - po.purchase_order_expected_date)::int
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		JOIN LATERAL (
			SELECT ` + outstandingLineExpr + ` AS outstanding
			FROM purchase_order_item poi
			WHERE poi.purchase_order_id = po.purchase_order_id AND poi.item_deleted_at IS NULL
		) line_out ON true
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY po.purchase_order_expected_date ASC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query overdue purchase orders: %w", err)
	}
	defer rows.Close()

	out := []OverduePO{}
	for rows.Next() {
		var o OverduePO
		if err := rows.Scan(&o.ID, &o.RecordNumber, &o.Vendor, &o.Value, &o.DaysOverdue); err != nil {
			return nil, fmt.Errorf("scan overdue purchase order: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query overdue purchase orders: %w", err)
	}
	return out, nil
}

// DashboardPendingApproval loads up to limit purchase orders awaiting
// approval that employeeID is a configured approver for (every pending
// order, unfiltered, when isSuperAdmin), oldest first, alongside the true
// total count/value across every matching order. Authorization here is
// approver-table membership, NOT generic RBAC resource read/scope --
// mirrors controllers.approvalModuleCountAndOldest exactly (see its doc
// comment for why: an approval queue is about who's configured to sign
// off, not who owns the record), so this widget's Pending tile agrees with
// the KPI strip's "Needs Approval" tile's purchasing contribution.
func DashboardPendingApproval(ctx context.Context, pool *pgxpool.Pool, employeeID int, isSuperAdmin bool, limit int) (PendingApprovalResult, error) {
	where := []string{"po.purchase_order_deleted_at IS NULL", "po.purchase_order_approval_status = 'pending'"}
	var args []any
	join := ""
	if !isSuperAdmin {
		recordTypeID, err := recordTypeIDByCode(ctx, pool, pordRecordTypeCode)
		if err != nil {
			return PendingApprovalResult{}, fmt.Errorf("resolve purchase order record type: %w", err)
		}
		eligible, err := isConfiguredApprover(ctx, pool, recordTypeID, employeeID)
		if err != nil {
			return PendingApprovalResult{}, fmt.Errorf("check purchase order approver eligibility: %w", err)
		}
		if !eligible {
			return PendingApprovalResult{Eligible: false}, nil
		}
		join = `JOIN purchase_order_approver ap ON ap.record_type_id = $1 AND ap.record_status_id = po.purchase_order_status
			AND ap.approver_employee_id = $2 AND ap.is_active`
		args = append(args, recordTypeID, employeeID)
	}

	q := `
		SELECT po.purchase_order_uuid, COALESCE(po.purchase_order_number, ''), po.purchase_order_vendor_name,
		       po.purchase_order_grand_total, po.purchase_order_created_at,
		       COUNT(*) OVER (), COALESCE(SUM(po.purchase_order_grand_total) OVER (), 0)
		FROM purchase_order po
		` + join + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY po.purchase_order_created_at ASC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return PendingApprovalResult{}, fmt.Errorf("query pending approval purchase orders: %w", err)
	}
	defer rows.Close()

	res := PendingApprovalResult{Eligible: true, Rows: []PendingApprovalPO{}}
	for rows.Next() {
		var p PendingApprovalPO
		if err := rows.Scan(&p.ID, &p.RecordNumber, &p.Vendor, &p.Value, &p.CreatedAt, &res.TotalCount, &res.TotalValue); err != nil {
			return PendingApprovalResult{}, fmt.Errorf("scan pending approval purchase order: %w", err)
		}
		res.Rows = append(res.Rows, p)
	}
	if err := rows.Err(); err != nil {
		return PendingApprovalResult{}, fmt.Errorf("query pending approval purchase orders: %w", err)
	}
	return res, nil
}

// isConfiguredApprover reports whether employeeID is an active approver for
// at least one purchase order status gate, regardless of current pending
// count -- mirrors controllers.approvalModuleCountAndOldest's own
// eligibility check exactly (same EXISTS-over-the-approver-table shape),
// duplicated here rather than imported since that helper is unexported in
// controllers and this package cannot depend on it (controllers already
// depends on purchaseorder, so the reverse import would cycle).
func isConfiguredApprover(ctx context.Context, pool *pgxpool.Pool, recordTypeID, employeeID int) (bool, error) {
	var eligible bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM purchase_order_approver WHERE record_type_id = $1 AND approver_employee_id = $2 AND is_active)`,
		recordTypeID, employeeID).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("query approver eligibility: %w", err)
	}
	return eligible, nil
}
