package docextractjob

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/vendorbill"
)

// PO-link tuning and reasons.
const (
	maxPOLinkCandidates = 200
	maxPOLinkRefRunes   = 40
	milliPerUnit        = 1000
	centsPerUnit        = 100

	reasonPOOtherVendor = "%s is for another vendor"
	reasonPOAmbiguous   = "%s matches more than one purchase order for this vendor"
	reasonPONotReceived = "%s has not received goods yet (status %s), so the bill cannot link to it"
	reasonPONothingLeft = "Nothing is left to bill on %s: everything received has already been billed"
	reasonPONoReceipts  = "Nothing has been received on %s yet, so there is nothing to bill"
)

// poNumberPrefixRe strips our own order-number prefix ("PORD-000123") so it
// compares equal to the "PO-123" a vendor prints.
var poNumberPrefixRe = regexp.MustCompile(`(?i)^\s*pord[\s\-#:.]*`)

// poNumberKey is the comparison key of a purchase order number or reference.
func poNumberKey(s string) string {
	return docextract.NormalizeDocNumber(poNumberPrefixRe.ReplaceAllString(s, ""))
}

// poLinkCandidate is a purchase order whose number or reference may match.
type poLinkCandidate struct {
	id           int
	uuid, number string
	reference    string
	status       string
	sameVendor   bool
}

// matchedByNumber reports whether the key equals our own PO number.
func (c poLinkCandidate) matchedByNumber(key string) bool { return poNumberKey(c.number) == key }

// matches reports whether the candidate's number or reference equals key.
func (c poLinkCandidate) matches(key string) bool {
	return c.matchedByNumber(key) || poNumberKey(c.reference) == key
}

// ResolveLinkedPO finds the purchase order a vendor bill's PO reference points
// at, for that vendor, by normalized number (our PO number or the vendor's
// reference number). It returns nil when the reference is blank, no vendor is
// resolved or no order matches. An order of another vendor, an ambiguous
// match, or one with nothing left to bill comes back with Billable=false and a
// Reason (and, for another vendor's order, neither uuid nor lines).
func (r *Resolver) ResolveLinkedPO(ctx context.Context, vendorUUID, docRef string) (*LinkedPO, error) {
	key := poNumberKey(docRef)
	if key == "" || !validUUID(vendorUUID) {
		return nil, nil
	}
	cands, err := r.loadPOCandidates(ctx, vendorUUID, key)
	if err != nil {
		return nil, err
	}
	label := clipRunes(strings.TrimSpace(docRef), maxPOLinkRefRunes)
	var mine, byNumber, others []poLinkCandidate
	for _, c := range cands {
		switch {
		case !c.matches(key):
		case !c.sameVendor:
			others = append(others, c)
		case c.matchedByNumber(key):
			byNumber = append(byNumber, c)
			mine = append(mine, c)
		default:
			mine = append(mine, c)
		}
	}
	pick := mine
	if len(byNumber) == 1 {
		pick = byNumber
	}
	switch {
	case len(pick) == 1:
		return r.describeLinkedPO(ctx, pick[0], label)
	case len(pick) > 1:
		return &LinkedPO{Reason: fmt.Sprintf(reasonPOAmbiguous, label), Lines: []LinkedPOLine{}}, nil
	case len(others) > 0:
		return &LinkedPO{Reason: fmt.Sprintf(reasonPOOtherVendor, label), Lines: []LinkedPOLine{}}, nil
	}
	return nil, nil
}

// loadPOCandidates loads live purchase orders whose number or reference
// contains key once separators are dropped (a superset of the exact match,
// which the caller applies in Go), this vendor's first.
func (r *Resolver) loadPOCandidates(ctx context.Context, vendorUUID, key string) ([]poLinkCandidate, error) {
	rows, err := r.db.Query(ctx, `
		SELECT po.purchase_order_id, po.purchase_order_uuid::text, COALESCE(po.purchase_order_number, ''),
		       po.purchase_order_reference_number, rs.record_status_code, (v.vendor_uuid = $1::uuid)
		FROM purchase_order po
		JOIN vendor v ON v.vendor_id = po.purchase_order_vendor_id
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_deleted_at IS NULL
		  AND (regexp_replace(lower(COALESCE(po.purchase_order_number, '')), '[^a-z0-9]', '', 'g') LIKE '%' || $2 || '%'
		    OR regexp_replace(lower(po.purchase_order_reference_number), '[^a-z0-9]', '', 'g') LIKE '%' || $2 || '%')
		ORDER BY (v.vendor_uuid = $1::uuid) DESC, po.purchase_order_id DESC
		LIMIT $3`, vendorUUID, key, maxPOLinkCandidates)
	if err != nil {
		return nil, fmt.Errorf("query purchase order candidates: %w", err)
	}
	defer rows.Close()
	var out []poLinkCandidate
	for rows.Next() {
		var c poLinkCandidate
		if err := rows.Scan(&c.id, &c.uuid, &c.number, &c.reference, &c.status, &c.sameVendor); err != nil {
			return nil, fmt.Errorf("scan purchase order candidate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate purchase order candidates: %w", err)
	}
	return out, nil
}

// describeLinkedPO loads the order's lines and decides whether a bill may link.
func (r *Resolver) describeLinkedPO(ctx context.Context, c poLinkCandidate, label string) (*LinkedPO, error) {
	lines, anyReceived, err := r.loadPOLines(ctx, c.id)
	if err != nil {
		return nil, err
	}
	out := &LinkedPO{UUID: c.uuid, Number: c.number, Status: c.status, Lines: lines}
	switch {
	case !vendorbill.IsConvertibleStatus(c.status):
		out.Reason = fmt.Sprintf(reasonPONotReceived, label, c.status)
	case !anyReceived:
		out.Reason = fmt.Sprintf(reasonPONoReceipts, label)
	case !anyBillable(lines):
		out.Reason = fmt.Sprintf(reasonPONothingLeft, label)
	default:
		out.Billable = true
	}
	return out, nil
}

// anyBillable reports whether any line has quantity left to bill.
func anyBillable(lines []LinkedPOLine) bool {
	for _, l := range lines {
		if l.BillableQtyMilli > 0 {
			return true
		}
	}
	return false
}

// loadPOLines reads the order's live lines with received and billed
// quantities through vendorbill.BillableLines, so the billed rule has a single
// copy (the one a conversion uses).
func (r *Resolver) loadPOLines(ctx context.Context, poID int) (lines []LinkedPOLine, anyReceived bool, err error) {
	src, err := vendorbill.BillableLines(ctx, r.db, poID)
	if err != nil {
		return nil, false, fmt.Errorf("load purchase order lines: %w", err)
	}
	lines = []LinkedPOLine{}
	for _, l := range src {
		received := milli(l.QtyReceived)
		if received > 0 {
			anyReceived = true
		}
		lines = append(lines, LinkedPOLine{
			ID: l.UUID, LineNumber: l.LineNumber, InventoryItemUUID: l.InventoryItemUUID,
			Description: l.Description, QtyMilli: milli(l.Qty),
			BillableQtyMilli: max(received-milli(l.QtyBilled), 0),
			UnitPriceCents:   int64(math.Round(l.UnitPrice * centsPerUnit)),
		})
	}
	return lines, anyReceived, nil
}

// milli converts a quantity to thousandths.
func milli(q float64) int64 { return int64(math.Round(q * milliPerUnit)) }

// clipRunes bounds s to n runes.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
