package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
)

// queryStatusReady is the only status filter GET /document-extractions accepts.
const queryStatusReady = "ready"

// docExtractFailureMessages maps a failure code to the user-facing message; %s
// is the uploaded file's name. Each states the reason and the next step.
var docExtractFailureMessages = map[string]string{
	string(docextract.FailCorrupt):               "We couldn't read %s because the file looks damaged. Re-export it and upload it again, or enter the order manually.",
	string(docextract.FailScanned):               "%s looks like a scanned image with no readable text. Upload a text-based PDF or DOCX, or enter the order manually.",
	string(docextract.FailUnreadableText):        "The text in %s can't be read reliably. Re-export it as a text-based PDF or DOCX, or enter the order manually.",
	string(docextract.FailPasswordProtected):     "%s is password protected. Remove the password and upload it again.",
	string(docextract.FailUnsupportedEncryption): "%s uses an unsupported kind of encryption. Remove the encryption and upload it again.",
	string(docextract.FailPageCap):               "%s has more pages than we can process. Upload only the order pages, or enter the order manually.",
	string(docextract.FailLineCap):               "%s has more line items than we can process. Split the order into smaller documents, or enter it manually.",
	string(docextract.FailTooLarge):              "%s is larger than the upload limit. Upload a smaller file, or enter the order manually.",
	string(docextract.FailUnsupportedType):       "%s is not a PDF or DOCX document. Upload a PDF or DOCX, or enter the order manually.",
	string(docextract.FailEmpty):                 "We found no text in %s. Upload a different file, or enter the order manually.",
	docextractjob.FailUploadMissing:              "The upload of %s didn't finish. Please retry the upload.",
	docextractjob.FailUploadCorrupted:            "%s was damaged during upload. Please retry the upload.",
	docextractjob.FailTransient:                  "We couldn't process %s right now. Please retry in a few minutes, or enter the order manually.",
	docextractjob.FailInternal:                   "Something went wrong while reading %s. Please retry, or enter the order manually.",
}

// docExtractFailureFallback is used for a failure code with no table entry.
const docExtractFailureFallback = "We couldn't process %s. Please retry, or enter the order manually."

// failureMessage returns the user-facing message for a failure code and file name.
func failureMessage(code, fileName string) string {
	if code == "" {
		return ""
	}
	format, ok := docExtractFailureMessages[code]
	if !ok {
		format = docExtractFailureFallback
	}
	return fmt.Sprintf(format, fileName)
}

// docExtractView is the owner-facing shape of an extraction. It deliberately
// has no staging key, content hash or owner field.
type docExtractView struct {
	ID             string                   `json:"id"`
	Status         string                   `json:"status"`
	DocType        string                   `json:"docType"`
	FileName       string                   `json:"fileName"`
	SizeBytes      int64                    `json:"sizeBytes"`
	Method         string                   `json:"method"`
	FailureCode    string                   `json:"failureCode,omitempty"`
	FailureMessage string                   `json:"failureMessage,omitempty"`
	RecordUUID     string                   `json:"recordUuid,omitempty"`
	CreatedAt      time.Time                `json:"createdAt"`
	ExpiresAt      time.Time                `json:"expiresAt"`
	Result         *docextractjob.ResultDoc `json:"result,omitempty"`
}

// newDocExtractView builds the view of ex without a result.
func newDocExtractView(ex *docextractjob.Extraction) docExtractView {
	return docExtractView{
		ID: ex.ID, Status: ex.Status, DocType: ex.DocType, FileName: ex.FileName, SizeBytes: ex.SizeBytes,
		Method: ex.Method, FailureCode: ex.FailureCode, FailureMessage: failureMessage(ex.FailureCode, ex.FileName),
		CreatedAt: ex.CreatedAt, ExpiresAt: ex.ExpiresAt,
	}
}

// duplicateResources maps a duplicate kind that links a record to the resource
// its read permission is checked against.
var duplicateResources = map[string]authz.Resource{
	docextractjob.DupSamePO:      authz.ResourceSalesOrder,
	docextractjob.DupRevision:    authz.ResourceSalesOrder,
	docextractjob.DupQuoteRef:    authz.ResourceQuote,
	docextractjob.DupEstimateRef: authz.ResourceEstimate,
}

// filterDuplicates keeps a duplicate's record link only when canRead allows the
// caller to read that record; otherwise uuid, number and status are blanked and
// the reason is swapped for a generic one, so a finding says a match exists
// without disclosing an out-of-scope record. It returns a non-nil slice.
func filterDuplicates(dups []docextractjob.Duplicate, canRead func(res authz.Resource, ownerUserID string) bool) []docextractjob.Duplicate {
	out := make([]docextractjob.Duplicate, 0, len(dups))
	for _, d := range dups {
		scopeOwner := d.OwnerUserID
		d.OwnerUserID = "" // server-side only: never part of a response
		if d.RecordUUID != "" {
			res, known := duplicateResources[d.Kind]
			if !known || !canRead(res, scopeOwner) {
				d.RecordUUID, d.Number, d.Status = "", "", ""
				d.Reason = docextractjob.GenericReason(d.Kind)
			}
		}
		out = append(out, d)
	}
	return out
}

// Get handles GET /api/tenant/document-extractions/{id}. Owner-only: another
// owner's id and an unknown id are both 404. A ready result is re-matched
// against current data first; duplicate record links are scope-filtered.
func (h *DocExtractOps) Get(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	c, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	ex, ok := h.loadOwned(w, r, c, r.PathValue("id"))
	if !ok {
		return
	}
	if !h.requireCreate(w, r, c, docextract.DocType(ex.DocType)) {
		return
	}
	view := newDocExtractView(ex)
	if len(ex.Result) > 0 {
		var doc docextractjob.ResultDoc
		if err := json.Unmarshal(ex.Result, &doc); err != nil {
			slog.ErrorContext(r.Context(), "doc extract: decode stored result", "extractionId", ex.ID, "error", err)
			fail(w, http.StatusInternalServerError, "Failed to load document extraction.")
			return
		}
		// A ready document may be resumed hours later: re-match it against
		// today's customers, items, aliases and orders. Best-effort — on error
		// the stored snapshot is still a valid answer.
		if fresh, err := docextractjob.Refresh(r.Context(), c.pool, ex, doc); err != nil {
			slog.WarnContext(r.Context(), "doc extract: refresh resolution", "extractionId", ex.ID, "error", err)
		} else {
			doc = fresh
		}
		doc.Duplicates = filterDuplicates(doc.Duplicates, h.newReadGate(r, c).canRead)
		view.Result = &doc
	}
	if ex.Status == docextractjob.StatusUsed || ex.Status == docextractjob.StatusAttached {
		if ex.RecordUUID != "" && canReadSO(r, c.pool, c.identityID, ex.RecordUUID) {
			view.RecordUUID = ex.RecordUUID
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "extraction": view})
}

// List handles GET /api/tenant/document-extractions?status=ready: the caller's
// own non-expired ready extractions (no result body), limited to the doc types
// the caller may create.
func (h *DocExtractOps) List(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	c, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("status") != queryStatusReady {
		fail(w, http.StatusBadRequest, "status=ready is required.")
		return
	}
	rows, err := c.store.ListReadyForOwner(r.Context(), c.identityID)
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: list ready", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to list document extractions.")
		return
	}
	allowed := map[string]bool{}
	out := make([]docExtractView, 0, len(rows))
	for i := range rows {
		ok, seen := allowed[rows[i].DocType]
		if !seen {
			ok = h.mayCreate(r, c, docextract.DocType(rows[i].DocType))
			allowed[rows[i].DocType] = ok
		}
		if ok {
			out = append(out, newDocExtractView(&rows[i]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "extractions": out})
}

// mayCreate quietly reports whether the caller holds create on the doc type's
// resource (no response is written; a lookup error fails closed).
func (h *DocExtractOps) mayCreate(r *http.Request, c *docExtractCaller, docType docextract.DocType) bool {
	res, ok := docExtractResources[docType]
	if !ok {
		return false
	}
	d, err := h.check(r.Context(), c, res, authz.ActionCreate)
	return err == nil && d.Allowed
}

// Notify handles POST /api/tenant/document-extractions/{id}/notify: asks for an
// in-app notification when the extraction finishes (the user closed the dialog).
// RBAC: Require(<doc type resource>, create); owner-only.
func (h *DocExtractOps) Notify(w http.ResponseWriter, r *http.Request) {
	c, ex, ok := h.authOwned(w, r, false)
	if !ok {
		return
	}
	if err := c.store.SetNotify(r.Context(), ex.ID, c.identityID, true); err != nil {
		h.failStoreMutation(w, r, ex.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// Discard handles POST /api/tenant/document-extractions/{id}/discard: marks the
// extraction discarded and deletes its staging object (best-effort).
// RBAC: Require(<doc type resource>, create); owner-only.
func (h *DocExtractOps) Discard(w http.ResponseWriter, r *http.Request) {
	c, ex, ok := h.authOwned(w, r, false)
	if !ok {
		return
	}
	key, err := c.store.Discard(r.Context(), ex.ID, c.identityID)
	if err != nil {
		h.failStoreMutation(w, r, ex.ID, err)
		return
	}
	ex.StagingKey = key
	h.deleteStaging(r.Context(), c.tenant, ex)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// failStoreMutation maps a Store state-change error: not found -> 404, wrong
// state -> 409 invalid_state, anything else -> 500.
func (h *DocExtractOps) failStoreMutation(w http.ResponseWriter, r *http.Request, id string, err error) {
	switch {
	case errors.Is(err, docextractjob.ErrNotFound):
		fail(w, http.StatusNotFound, "Document extraction not found.")
	case errors.Is(err, docextractjob.ErrConflict):
		failWithCode(w, http.StatusConflict, "This document can no longer be changed.", codeInvalidState)
	default:
		slog.ErrorContext(r.Context(), "doc extract: update extraction", "extractionId", id, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to update document extraction.")
	}
}
