package doclinks

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Record is one document reference, matching the fields a list row returns.
type Record struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	StatusCode string `json:"statusCode"`
	Status     string `json:"status"`
}

// Stage is one step of the flow. Records is never null: an empty array means
// nothing exists at that stage yet. Hidden is true when records the caller may
// not see were left out, without revealing which.
type Stage struct {
	Key     string   `json:"key"`
	Records []Record `json:"records"`
	Hidden  bool     `json:"hidden,omitempty"`
}

// Block is the read-only `workflow` object added to a detail response.
type Block struct {
	Stages []Stage `json:"stages"`
}

// Access says what the caller may see of one stage.
type Access struct {
	// Allowed is false when the caller has no read grant on the module.
	Allowed bool
	// OwnOnly narrows an allowed grant to records the caller owns.
	OwnOnly bool
}

// Visibility maps a stage kind to the caller's access; a missing kind is denied.
type Visibility map[Kind]Access

// Querier is the slice of pgx the package needs; *pgxpool.Pool and pgx.Tx satisfy it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNotFound means the viewed record does not exist.
var ErrNotFound = errors.New("doclinks: record not found")

// table describes where one stage's rows live.
type table struct {
	kind                                                    Kind
	name, idCol, uuidCol, numCol, statusCol, owner, deleted string
}

var salesStages = []table{
	{KindQuote, "quote", "quote_id", "quote_uuid", "quote_number", "quote_status", "quote_owner_id", "quote_deleted_at"},
	{KindSalesOrder, "sales_order", "sales_order_id", "sales_order_uuid", "sales_order_number", "sales_order_status", "sales_order_owner_id", "sales_order_deleted_at"},
	{KindFabrication, "fabrication_job", "fabrication_job_id", "fabrication_job_uuid", "fabrication_job_number", "fabrication_job_status", "job_owner_id", "fabrication_job_deleted_at"},
	{KindInvoice, "invoice", "invoice_id", "invoice_uuid", "invoice_number", "invoice_status", "invoice_owner_id", "invoice_deleted_at"},
	{KindPayment, "payment", "payment_id", "payment_uuid", "payment_number", "payment_status", "payment_owner_id", "payment_deleted_at"},
	{KindRefund, "refund", "refund_id", "refund_uuid", "refund_number", "refund_status", "refund_owner_id", "refund_deleted_at"},
}

// purchaseStages: a requisition has no owner column, its requester is the scope owner.
var purchaseStages = []table{
	{KindRequisition, "requisition", "requisition_id", "requisition_uuid", "requisition_number", "requisition_status", "requisition_requested_by_id", "requisition_deleted_at"},
	{KindPurchaseOrd, "purchase_order", "purchase_order_id", "purchase_order_uuid", "purchase_order_number", "purchase_order_status", "purchase_order_owner_id", "purchase_order_deleted_at"},
	{KindItemReceipt, "item_receipt", "item_receipt_id", "item_receipt_uuid", "item_receipt_number", "item_receipt_status", "item_receipt_owner_id", "item_receipt_deleted_at"},
	{KindVendorBill, "vendor_bill", "vendor_bill_id", "vendor_bill_uuid", "vendor_bill_number", "vendor_bill_status", "vendor_bill_owner_id", "vendor_bill_deleted_at"},
	{KindVendorPay, "vendor_payment", "vendor_payment_id", "vendor_payment_uuid", "vendor_payment_number", "vendor_payment_status", "vendor_payment_owner_id", "vendor_payment_deleted_at"},
}

// allStages is every table the package reads.
var allStages = append(append([]table{}, salesStages...), purchaseStages...)

// flowOf returns the stage list of the flow a kind belongs to.
func flowOf(k Kind) []table {
	for _, t := range purchaseStages {
		if t.kind == k {
			return purchaseStages
		}
	}
	return salesStages
}

func tableOf(k Kind) (table, bool) {
	for _, t := range allStages {
		if t.kind == k {
			return t, true
		}
	}
	return table{}, false
}

// pgLoader runs edge hops against Postgres.
type pgLoader struct{ q Querier }

func (p pgLoader) Hop(ctx context.Context, e edge, from []int32) ([]int32, error) {
	rows, err := p.q.Query(ctx, e.sql, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Resolve returns the workflow block for the record `uuid` of `kind`. viewerUserID
// is the caller's tenant users.id, used to narrow own-only stages.
func Resolve(ctx context.Context, q Querier, kind Kind, uuid, viewerUserID string, vis Visibility) (Block, error) {
	t, ok := tableOf(kind)
	if !ok {
		return Block{}, fmt.Errorf("doclinks: %s is not a flow stage", kind)
	}
	var id int32
	err := q.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1 AND %s IS NULL`,
		t.idCol, t.name, t.uuidCol, t.deleted), uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Block{}, ErrNotFound
	}
	if err != nil {
		return Block{}, fmt.Errorf("resolve %s %s: %w", kind, uuid, err)
	}
	chain, err := Walk(ctx, pgLoader{q}, kind, id)
	if err != nil {
		return Block{}, err
	}
	flow := flowOf(kind)
	block := Block{Stages: make([]Stage, 0, len(flow))}
	for _, st := range flow {
		stage, err := loadStage(ctx, q, st, chain.ids(st.kind), viewerUserID, vis[st.kind])
		if err != nil {
			return Block{}, err
		}
		block.Stages = append(block.Stages, stage)
	}
	return block, nil
}

// loadStage reads one stage's rows, dropping what the caller may not see.
func loadStage(ctx context.Context, q Querier, t table, ids []int32, viewerUserID string, acc Access) (Stage, error) {
	stage := Stage{Key: string(t.kind), Records: []Record{}}
	if len(ids) == 0 {
		return stage, nil
	}
	rows, err := q.Query(ctx, fmt.Sprintf(`
		SELECT t.%s::text, COALESCE(t.%s, ''), rs.record_status_code, rs.record_status_name,
		       COALESCE(u.id::text, '') = $2
		FROM %s t
		JOIN lkp_record_status rs ON rs.record_status_id = t.%s
		LEFT JOIN employee e ON e.employee_id = t.%s
		LEFT JOIN users u ON u.id = e.employee_user_id
		WHERE t.%s = ANY($1) AND t.%s IS NULL
		ORDER BY t.%s`, t.uuidCol, t.numCol, t.name, t.statusCol, t.owner, t.idCol, t.deleted, t.idCol), ids, viewerUserID)
	if err != nil {
		return Stage{}, fmt.Errorf("load %s records: %w", t.kind, err)
	}
	defer rows.Close()
	for rows.Next() {
		var r Record
		var owned bool
		if err := rows.Scan(&r.ID, &r.Number, &r.StatusCode, &r.Status, &owned); err != nil {
			return Stage{}, fmt.Errorf("scan %s record: %w", t.kind, err)
		}
		if !acc.Allowed || (acc.OwnOnly && !owned) {
			stage.Hidden = true
			continue
		}
		stage.Records = append(stage.Records, r)
	}
	if err := rows.Err(); err != nil {
		return Stage{}, fmt.Errorf("read %s records: %w", t.kind, err)
	}
	return stage, nil
}
