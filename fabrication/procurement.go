package fabrication

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
)

// ErrPurchaseReadPermission rejects linked-order reads without procurement access.
var ErrPurchaseReadPermission = errors.New("purchase order read permission required")

// ProcurementLine reports posted receipt quantities in the order line's own unit.
type ProcurementLine struct {
	Number   int     `json:"number"`
	Material string  `json:"material"`
	Unit     string  `json:"unit"`
	Ordered  float64 `json:"ordered"`
	Received float64 `json:"received"`
}

// ProcurementOrder is a scope-filtered linked purchase order, including historical revisions.
type ProcurementOrder struct {
	ID               string            `json:"id"`
	Number           string            `json:"number"`
	Status           string            `json:"status"`
	StatusCode       string            `json:"statusCode"`
	Vendor           string            `json:"vendor"`
	ExpectedDate     string            `json:"expectedDate"`
	TemplateRevision int               `json:"templateRevision"`
	TemplateState    string            `json:"templateState"`
	Reason           string            `json:"reason"`
	Lines            []ProcurementLine `json:"lines"`
}

// ProcurementOrders requires an already authorized job read; PO scope is independently enforced.
func ProcurementOrders(ctx context.Context, pool *pgxpool.Pool, jobID, identity string) ([]ProcurementOrder, error) {
	decision, err := authz.Check(ctx, pool, identity, authz.ResourcePurchaseOrder, authz.ActionRead)
	if err != nil {
		return nil, fmt.Errorf("authorize linked purchase orders: %w", err)
	}
	if !decision.Allowed {
		return nil, ErrPurchaseReadPermission
	}
	return procurementOrders(ctx, pool, jobID, identity, decision.Scope == authz.ScopeAll)
}

func procurementOrders(ctx context.Context, pool *pgxpool.Pool, jobID, identity string, all bool) ([]ProcurementOrder, error) {
	rows, err := pool.Query(ctx, `SELECT po.purchase_order_uuid,COALESCE(po.purchase_order_number,''),rs.record_status_name,rs.record_status_code,po.purchase_order_vendor_name,COALESCE(to_char(po.purchase_order_expected_date,'YYYY-MM-DD'),''),t.revision,t.state,p.reason,COALESCE(i.line_number,0),COALESCE(i.item_name,''),COALESCE(i.unit_code,''),COALESCE(i.quantity,0),COALESCE(i.qty_received,0)
 FROM fabrication_shortage_purchase p
 JOIN fabrication_job j ON j.fabrication_job_id=p.fabrication_job_id
 JOIN fabrication_template_revision t ON t.template_id=p.template_id
 JOIN purchase_order po ON po.purchase_order_id=p.purchase_order_id
 JOIN lkp_record_status rs ON rs.record_status_id=po.purchase_order_status
 LEFT JOIN employee e ON e.employee_id=po.purchase_order_owner_id
 LEFT JOIN users u ON u.id=e.employee_user_id
 LEFT JOIN purchase_order_item i ON i.purchase_order_id=po.purchase_order_id AND i.item_deleted_at IS NULL
 WHERE j.fabrication_job_uuid=$1 AND j.fabrication_job_deleted_at IS NULL AND po.purchase_order_deleted_at IS NULL AND ($3 OR u.identity_id=$2)
 ORDER BY p.created_at DESC,po.purchase_order_id DESC,i.line_number`, jobID, identity, all)
	if err != nil {
		return nil, fmt.Errorf("read linked purchase orders: %w", err)
	}
	defer rows.Close()
	out := []ProcurementOrder{}
	for rows.Next() {
		var order ProcurementOrder
		var line ProcurementLine
		if err = rows.Scan(&order.ID, &order.Number, &order.Status, &order.StatusCode, &order.Vendor, &order.ExpectedDate, &order.TemplateRevision, &order.TemplateState, &order.Reason, &line.Number, &line.Material, &line.Unit, &line.Ordered, &line.Received); err != nil {
			return nil, fmt.Errorf("scan linked purchase order: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].ID != order.ID {
			order.Lines = []ProcurementLine{}
			out = append(out, order)
		}
		if line.Number > 0 {
			out[len(out)-1].Lines = append(out[len(out)-1].Lines, line)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate linked purchase orders: %w", err)
	}
	return out, nil
}
