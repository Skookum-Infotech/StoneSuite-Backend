package controllers

import (
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
)

// linkedPOHiddenReason is shown instead of the purchase order's details when
// the caller may not read it.
const linkedPOHiddenReason = "A purchase order is referenced but you can't view it"

// filterLinkedPO hides the purchase order a bill resolved to when canRead
// denies the caller: uuid, number, status and lines are blanked and the link is
// marked not billable so no line can be tied to a record the caller can't see.
// ownerUserID is the order's owner; ok is false when it could not be loaded
// (which fails closed). It returns the same pointer for a nil input.
func filterLinkedPO(po *docextractjob.LinkedPO, canRead func(res authz.Resource, ownerUserID string) bool, ownerUserID string, ok bool) *docextractjob.LinkedPO {
	if po == nil {
		return nil
	}
	if po.UUID == "" {
		// An "another vendor" or "ambiguous" reason comes from purchase orders
		// matched tenant-wide; naming them to a caller below `all` scope would
		// confirm orders they can't see. An empty owner is in scope only at `all`.
		if po.Reason != "" && !canRead(authz.ResourcePurchaseOrder, "") {
			return &docextractjob.LinkedPO{Billable: false, Reason: linkedPOHiddenReason, Lines: []docextractjob.LinkedPOLine{}}
		}
		if po.Lines == nil {
			po.Lines = []docextractjob.LinkedPOLine{}
		}
		return po
	}
	if ok && canRead(authz.ResourcePurchaseOrder, ownerUserID) {
		if po.Lines == nil {
			po.Lines = []docextractjob.LinkedPOLine{}
		}
		return po
	}
	return &docextractjob.LinkedPO{Billable: false, Reason: linkedPOHiddenReason, Lines: []docextractjob.LinkedPOLine{}}
}

// normalizeResultDoc turns nil slices into empty ones so the wire never
// carries null for lines or duplicates.
func normalizeResultDoc(doc *docextractjob.ResultDoc) {
	if doc.Extracted.Lines == nil {
		doc.Extracted.Lines = []docextract.Line{}
	}
	if doc.Resolution.Lines == nil {
		doc.Resolution.Lines = []docextractjob.LineMatch{}
	}
	if doc.Duplicates == nil {
		doc.Duplicates = []docextractjob.Duplicate{}
	}
	if doc.Resolution.LinkedPO != nil && doc.Resolution.LinkedPO.Lines == nil {
		doc.Resolution.LinkedPO.Lines = []docextractjob.LinkedPOLine{}
	}
}

// scopeResultDoc applies the caller's read scope to every record link in doc:
// duplicates and the bill's linked purchase order.
func (h *DocExtractOps) scopeResultDoc(r *http.Request, c *docExtractCaller, doc *docextractjob.ResultDoc) {
	gate := h.newReadGate(r, c)
	doc.Duplicates = filterDuplicates(doc.Duplicates, gate.canRead)
	if po := doc.Resolution.LinkedPO; po != nil {
		var owner string
		ok := false
		if po.UUID != "" {
			var err error
			owner, _, err = h.recordOwner(r.Context(), c, docextract.DocTypePurchaseOrder, po.UUID)
			ok = err == nil
		}
		doc.Resolution.LinkedPO = filterLinkedPO(po, gate.canRead, owner, ok)
	}
	normalizeResultDoc(doc)
}

// canReadCreatedRecord reports whether the caller may read the record an
// extraction created, by the doc type's resource read scope.
func (h *DocExtractOps) canReadCreatedRecord(r *http.Request, c *docExtractCaller, docType docextract.DocType, uuid string) bool {
	res, ok := docExtractResources[docType]
	if !ok {
		return false
	}
	owner, _, err := h.recordOwner(r.Context(), c, docType, uuid)
	return err == nil && h.newReadGate(r, c).canRead(res, owner)
}
