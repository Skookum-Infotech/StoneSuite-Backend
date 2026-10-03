package docextractjob

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/workflow"
)

// Sales-order statuses (lkp_record_status codes, record type SORD) that mean
// the order can no longer take a revision.
var closedSalesOrderStatuses = map[string]struct{}{"CANC": {}, "FILL": {}}

// maxSalesOrderCandidates bounds the per-customer sales-order scan.
const maxSalesOrderCandidates = 500

// Duplicate reasons.
const (
	reasonSamePO         = "Possible duplicate of %s (same PO number)"
	reasonSamePOAndTotal = "Possible duplicate of %s (same PO number, same total)"
	reasonRevisionOpen   = "This looks like %s of %s (%s)"
	reasonRevisionClosed = "This looks like %s of %s (%s). The order is closed, so the revision must be handled manually (credit memo or change)"
	reasonReference      = "References %s (%s) - open it to convert instead?"
)

// Checker runs the duplicate, revision and reference lookups on a tenant pool.
type Checker struct{ db workflow.Querier }

// NewChecker builds a Checker over a tenant pool or transaction.
func NewChecker(db workflow.Querier) *Checker { return &Checker{db: db} }

// DuplicateInput is what the sales-order checks need from the extraction.
type DuplicateInput struct {
	CustomerUUID string // resolved customer; "" skips the customer-scoped checks
	PONumber     string
	TotalCents   int64
	HasTotal     bool
	Revision     *docextract.Revision
	QuoteRef     string
}

// soCandidate is one sales order considered for a duplicate / revision match.
type soCandidate struct {
	uuid, number, po, statusCode, statusName, ownerUserID string
	totalCents                                            int64
	sameCustomer                                          bool
}

// Check returns the sales-order duplicate / revision findings and the
// quote/estimate reference findings. It does not include same-file hits (see
// Store.DuplicateBySHA).
func (c *Checker) Check(ctx context.Context, in DuplicateInput) ([]Duplicate, error) {
	var out []Duplicate
	so, err := c.salesOrderFindings(ctx, in)
	if err != nil {
		return nil, err
	}
	out = append(out, so...)
	if in.QuoteRef != "" {
		refs, err := c.References(ctx, in.QuoteRef)
		if err != nil {
			return nil, err
		}
		out = append(out, refs...)
	}
	return out, nil
}

// salesOrderFindings finds same-PO duplicates and revision targets.
func (c *Checker) salesOrderFindings(ctx context.Context, in DuplicateInput) ([]Duplicate, error) {
	poKey := docextract.NormalizeDocNumber(in.PONumber)
	var refKey string
	var refRaw []string
	if in.Revision != nil && in.Revision.ReferencedNumber != "" {
		refKey = docextract.NormalizeDocNumber(in.Revision.ReferencedNumber)
		refRaw = []string{strings.ToUpper(strings.TrimSpace(in.Revision.ReferencedNumber))}
	}
	hasCustomer := validUUID(in.CustomerUUID)
	if (poKey == "" || !hasCustomer) && refKey == "" {
		return nil, nil
	}
	cands, err := c.loadSalesOrders(ctx, in.CustomerUUID, refRaw)
	if err != nil {
		return nil, err
	}
	return classifySalesOrders(cands, in, poKey, refKey), nil
}

// classifySalesOrders is the pure matching rule over loaded candidates: a
// revision when the document says so and the order matches by customer+PO or by
// referenced number, else a same-PO duplicate (total as an extra signal).
func classifySalesOrders(cands []soCandidate, in DuplicateInput, poKey, refKey string) []Duplicate {
	var out []Duplicate
	for _, so := range cands {
		samePO := so.sameCustomer && poKey != "" && docextract.NormalizeDocNumber(so.po) == poKey
		byRef := refKey != "" && (docextract.NormalizeDocNumber(so.number) == refKey ||
			(so.sameCustomer && docextract.NormalizeDocNumber(so.po) == refKey))
		switch {
		case in.Revision != nil && (samePO || byRef):
			out = append(out, revisionDuplicate(so, in.Revision))
		case samePO:
			reason := fmt.Sprintf(reasonSamePO, so.number)
			if in.HasTotal && so.totalCents == in.TotalCents {
				reason = fmt.Sprintf(reasonSamePOAndTotal, so.number)
			}
			out = append(out, Duplicate{Kind: DupSamePO, RecordUUID: so.uuid, Number: so.number, Status: so.statusName,
				Reason: reason, OwnerUserID: so.ownerUserID})
		}
	}
	return out
}

// revisionDuplicate builds the revision finding for an existing order.
func revisionDuplicate(so soCandidate, rev *docextract.Revision) Duplicate {
	reason := fmt.Sprintf(reasonRevisionOpen, rev.Label, so.number, so.statusName)
	if _, closed := closedSalesOrderStatuses[so.statusCode]; closed {
		reason = fmt.Sprintf(reasonRevisionClosed, rev.Label, so.number, so.statusName)
	}
	return Duplicate{Kind: DupRevision, RecordUUID: so.uuid, Number: so.number, Status: so.statusName,
		Reason: reason, OwnerUserID: so.ownerUserID}
}

// loadSalesOrders loads the customer's live sales orders plus any order whose
// number equals one of refNumbers (upper-cased).
func (c *Checker) loadSalesOrders(ctx context.Context, customerUUID string, refNumbers []string) ([]soCandidate, error) {
	var custArg *string
	if validUUID(customerUUID) {
		custArg = &customerUUID
	}
	rows, err := c.db.Query(ctx, `
		SELECT so.sales_order_uuid::text, COALESCE(so.sales_order_number, ''), so.sales_order_po_number,
		       ROUND(so.sales_order_grand_total * 100)::bigint,
		       rs.record_status_code, rs.record_status_name, COALESCE(ou.id::text, ''),
		       (c.customer_uuid = $1::uuid)
		FROM sales_order so
		JOIN customer c ON c.customer_id = so.sales_order_customer_id
		JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		LEFT JOIN employee oe ON oe.employee_id = so.sales_order_owner_id
		LEFT JOIN users ou ON ou.id = oe.employee_user_id
		WHERE so.sales_order_deleted_at IS NULL
		  AND (c.customer_uuid = $1::uuid OR upper(so.sales_order_number) = ANY($2))
		ORDER BY so.sales_order_created_at DESC LIMIT $3`,
		custArg, refNumbers, maxSalesOrderCandidates)
	if err != nil {
		return nil, fmt.Errorf("query sales orders for duplicates: %w", err)
	}
	defer rows.Close()
	var out []soCandidate
	for rows.Next() {
		var s soCandidate
		var same *bool
		if err := rows.Scan(&s.uuid, &s.number, &s.po, &s.totalCents, &s.statusCode, &s.statusName, &s.ownerUserID, &same); err != nil {
			return nil, fmt.Errorf("scan sales order candidate: %w", err)
		}
		s.sameCustomer = same != nil && *same
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sales order candidates: %w", err)
	}
	return out, nil
}

// Generic duplicate reasons, used when the caller cannot read the matched
// record: they say a match exists without naming its number or status.
const (
	genericReasonSamePO    = "Possible duplicate of an existing sales order (same PO number)"
	genericReasonRevision  = "This looks like a revision of an existing sales order"
	genericReasonReference = "References an existing quote or estimate"
	genericReasonDefault   = "Possible duplicate of an existing record"
)

// GenericReason returns a reason for kind that names no record, for callers
// outside the matched record's read scope.
func GenericReason(kind string) string {
	switch kind {
	case DupSamePO:
		return genericReasonSamePO
	case DupRevision:
		return genericReasonRevision
	case DupQuoteRef, DupEstimateRef:
		return genericReasonReference
	case DupSameFile:
		return reasonSameFile
	default:
		return genericReasonDefault
	}
}
