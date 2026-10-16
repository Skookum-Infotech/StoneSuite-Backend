package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Eligibility reasons returned by arEligible; "" means the invoice may be dunned.
const (
	reasonVoided        = "voided"
	reasonPaid          = "paid"
	reasonDoNotContact  = "do_not_contact"
	reasonARHold        = "ar_hold"
	reasonNotSent       = "not_sent"
	reasonAnchorChanged = "anchor_changed"
	reasonNoEmail       = "no_email"
	reasonCreditCovers  = "credit_covers"
)

const (
	// invoiceWorkflowKey is the document_sends.workflow_key controllers/documents.go
	// writes for an invoice send (DocMeta.WorkflowKey).
	invoiceWorkflowKey = "invoice"
	statusVoid         = "VOID"
	statusPaid         = "PAID"
	// candidateLookbackDays / candidateLookaheadDays bound the candidate scan:
	// older invoices are "stale" in the planner anyway.
	candidateLookbackDays  = 60
	candidateLookaheadDays = 3
)

// outstandingARStatuses mirrors invoice.outstandingStatusCodes (invoice/aging.go):
// billed but not settled.
var outstandingARStatuses = []string{"SENT", "PART", "ODUE"}

// arHoldStatuses are customer_ar_status codes that suspend dunning.
var arHoldStatuses = []string{"CRHD", "COLL", "BDBT"}

// Querier is the small pgx surface the AR source needs (satisfied by *pgxpool.Pool and pgx.Tx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ARInvoice is one receivable that may need a payment reminder. Anchor is the
// invoice due date; Balance is the live balance_due as a decimal string.
type ARInvoice struct {
	UUID            string
	Number          string
	CustomerName    string
	Email           string
	OwnerEmployeeID *int
	Anchor          time.Time
	Balance         string
}

// arSelect is the shared projection: invoice + customer facts, with the
// recipient (customer_accounts_email, else invoice_bill_email) trimmed and
// lower-cased because the weekly-cap index relies on lower-case addresses.
const arSelect = `
SELECT i.invoice_uuid::text, COALESCE(i.invoice_number, ''), c.customer_name,
       lower(btrim(COALESCE(NULLIF(btrim(c.customer_accounts_email), ''),
                            NULLIF(btrim(i.invoice_bill_email), ''), ''))),
       i.invoice_owner_id, i.invoice_due_date, i.invoice_balance_due::text
FROM invoice i
JOIN customer c ON c.customer_id = i.invoice_customer_id
JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
`

// listARCandidates returns outstanding invoices due within [today-60d, today+3d]
// that were sent at least once. It deliberately does not filter on
// do-not-contact or AR hold: the planner records those as skipped rows and
// arEligible rechecks them live.
func listARCandidates(ctx context.Context, q Querier, today time.Time) ([]ARInvoice, error) {
	rows, err := q.Query(ctx, arSelect+`
WHERE rs.record_status_code = ANY($1)
  AND i.invoice_balance_due > 0
  AND i.invoice_due_date IS NOT NULL
  AND i.invoice_due_date BETWEEN $2::date - $3::int AND $2::date + $4::int
  AND i.invoice_deleted_at IS NULL
  AND c.customer_deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM document_sends ds
              WHERE ds.record_id = i.invoice_uuid AND ds.workflow_key = $5)
ORDER BY i.invoice_due_date, i.invoice_id`,
		outstandingARStatuses, today, candidateLookbackDays, candidateLookaheadDays, invoiceWorkflowKey)
	if err != nil {
		return nil, fmt.Errorf("list ar candidates: %w", err)
	}
	defer rows.Close()
	var out []ARInvoice
	for rows.Next() {
		var inv ARInvoice
		if err := rows.Scan(&inv.UUID, &inv.Number, &inv.CustomerName, &inv.Email,
			&inv.OwnerEmployeeID, &inv.Anchor, &inv.Balance); err != nil {
			return nil, fmt.Errorf("scan ar candidate: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ar candidates: %w", err)
	}
	return out, nil
}

// arFacts are the live predicates arEligible evaluates; money comparisons are
// done in SQL (numeric) so no float rounding is involved.
type arFacts struct {
	deleted, customerDeleted, doNotContact, owes, hasSend, creditCovers bool
	statusCode                                                          string
	arStatus                                                            *string
}

// arEligible re-checks one invoice live at send time. reason "" means eligible;
// reasons are evaluated in a fixed precedence (first match wins). live carries
// the current balance and due date.
//
// The customer's credit is the same available-credit definition the refund
// module documents (schema.sql refund header): payment_unapplied_amount -
// payment_refunded_total - payment_credited_total over non-VOID live payments,
// plus credit_memo_unapplied_amount - credit_memo_refunded_total over APPV
// (appliable, see creditmemo/apply.go) live credit memos, each clamped at 0.
func arEligible(ctx context.Context, q Querier, invoiceUUID string, plannedAnchor, _ time.Time) (ok bool, reason string, live ARInvoice, err error) {
	var f arFacts
	var dueDate *time.Time
	row := q.QueryRow(ctx, `
SELECT i.invoice_uuid::text, COALESCE(i.invoice_number, ''), c.customer_name,
       lower(btrim(COALESCE(NULLIF(btrim(c.customer_accounts_email), ''),
                            NULLIF(btrim(i.invoice_bill_email), ''), ''))),
       i.invoice_owner_id, i.invoice_due_date, i.invoice_balance_due::text,
       rs.record_status_code, ars.customer_ar_status_code,
       (i.invoice_deleted_at IS NOT NULL), (c.customer_deleted_at IS NOT NULL),
       c.customer_do_not_contact, (i.invoice_balance_due > 0),
       EXISTS (SELECT 1 FROM document_sends ds
               WHERE ds.record_id = i.invoice_uuid AND ds.workflow_key = $2),
       (
         COALESCE((SELECT SUM(GREATEST(p.payment_unapplied_amount - p.payment_refunded_total - p.payment_credited_total, 0))
                   FROM payment p JOIN lkp_record_status ps ON ps.record_status_id = p.payment_status
                   WHERE p.payment_customer_id = i.invoice_customer_id
                     AND p.payment_deleted_at IS NULL AND ps.record_status_code <> $3), 0)
         + COALESCE((SELECT SUM(GREATEST(m.credit_memo_unapplied_amount - m.credit_memo_refunded_total, 0))
                   FROM credit_memo m JOIN lkp_record_status ms ON ms.record_status_id = m.credit_memo_status
                   WHERE m.credit_memo_customer_id = i.invoice_customer_id
                     AND m.credit_memo_deleted_at IS NULL AND ms.record_status_code = $4), 0)
       ) >= i.invoice_balance_due
FROM invoice i
JOIN customer c ON c.customer_id = i.invoice_customer_id
JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
LEFT JOIN lkp_customer_ar_status ars ON ars.customer_ar_status_id = c.customer_ar_status
WHERE i.invoice_uuid = $1::uuid`,
		invoiceUUID, invoiceWorkflowKey, statusVoid, creditMemoApplicableStatus)
	scanErr := row.Scan(&live.UUID, &live.Number, &live.CustomerName, &live.Email,
		&live.OwnerEmployeeID, &dueDate, &live.Balance, &f.statusCode, &f.arStatus,
		&f.deleted, &f.customerDeleted, &f.doNotContact, &f.owes, &f.hasSend, &f.creditCovers)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return false, reasonVoided, ARInvoice{}, nil
	}
	if scanErr != nil {
		return false, "", ARInvoice{}, fmt.Errorf("ar eligibility %s: %w", invoiceUUID, scanErr)
	}
	if dueDate != nil {
		live.Anchor = *dueDate
	}
	anchorChanged := dueDate == nil || !sameDay(*dueDate, plannedAnchor)
	reason = arReason(f, anchorChanged, live.Email)
	return reason == "", reason, live, nil
}

// creditMemoApplicableStatus is the credit memo status that still holds spendable credit.
const creditMemoApplicableStatus = "APPV"

// arReason maps live facts to the first matching skip reason ("" = eligible).
func arReason(f arFacts, anchorChanged bool, email string) string {
	switch {
	case f.deleted || f.customerDeleted || f.statusCode == statusVoid:
		return reasonVoided
	case f.statusCode == statusPaid || !f.owes:
		return reasonPaid
	case f.doNotContact:
		return reasonDoNotContact
	case f.arStatus != nil && containsString(arHoldStatuses, *f.arStatus):
		return reasonARHold
	case !f.hasSend:
		return reasonNotSent
	case anchorChanged:
		return reasonAnchorChanged
	case email == "":
		return reasonNoEmail
	case f.creditCovers:
		return reasonCreditCovers
	}
	return ""
}

// sameDay reports whether two dates fall on the same calendar day (ignoring zone offsets of the DATE scan).
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func containsString(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}
