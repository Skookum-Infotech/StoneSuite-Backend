package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/workflow"
)

// docExtractUUIDRe matches a canonical UUID; a client-supplied record id is
// checked with it so a malformed one is a 404, never a database cast error.
var docExtractUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type docExtractCompleteRequest struct {
	RecordUUID string                    `json:"recordUuid"`
	Saved      docextractjob.SavedValues `json:"saved"`
}

// Complete handles POST /api/tenant/document-extractions/{id}/complete: after
// the user saved the record, marks the extraction used (CAS ready -> used),
// records the learned aliases and feedback, and audits the creation on the
// record's own trail. The record must exist, match the doc type and have been
// created by the caller; otherwise 404. A second call is 409 already_used.
// RBAC: Require(<doc type resource>, create); owner-only.
func (h *DocExtractOps) Complete(w http.ResponseWriter, r *http.Request) {
	c, ex, ok := h.authOwned(w, r, false)
	if !ok {
		return
	}
	var req docExtractCompleteRequest
	if !decodeDocExtractBody(w, r, &req) {
		return
	}
	if ex.Status == docextractjob.StatusUsed || ex.Status == docextractjob.StatusAttached {
		failWithCode(w, http.StatusConflict, "This document was already used to create a record.", codeAlreadyUsed)
		return
	}
	if ex.Status != docextractjob.StatusReady {
		failWithCode(w, http.StatusConflict, "This document is not ready to be completed.", codeInvalidState)
		return
	}
	if !docExtractUUIDRe.MatchString(req.RecordUUID) {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	if !h.callerCreatedRecord(w, r, c, ex, req.RecordUUID) {
		return
	}
	if err := c.store.CompleteCAS(r.Context(), ex.ID, c.identityID, req.RecordUUID); err != nil {
		if errors.Is(err, docextractjob.ErrConflict) {
			failWithCode(w, http.StatusConflict, "This document was already used to create a record.", codeAlreadyUsed)
			return
		}
		h.failStoreMutation(w, r, ex.ID, err)
		return
	}
	// The CAS already succeeded: learning and audit are best-effort and must
	// never fail the request.
	if err := docextractjob.RecordCompletion(r.Context(), c.pool, ex.ID, c.identityID, req.RecordUUID, req.Saved); err != nil {
		slog.ErrorContext(r.Context(), "doc extract: record completion", "extractionId", ex.ID, "error", err)
	}
	h.auditCreatedFromDocument(r, c, ex, req.RecordUUID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// callerCreatedRecord verifies the claimed record is a live sales order of the
// extraction's doc type that the caller created (an order has no created-by on
// its API shape; its owner defaults to the creator, so the owner is the caller's user). Any failure
// is a 404, logged as doc_extract_idor when the record exists but isn't theirs.
func (h *DocExtractOps) callerCreatedRecord(w http.ResponseWriter, r *http.Request, c *docExtractCaller, ex *docextractjob.Extraction, recordUUID string) bool {
	if docextract.DocType(ex.DocType) != docextract.DocTypeSalesOrder {
		fail(w, http.StatusNotFound, "Record not found.")
		return false
	}
	order, err := salesorder.Get(r.Context(), c.pool, recordUUID)
	if errors.Is(err, salesorder.ErrNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: load record to complete", "extractionId", ex.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to complete document extraction.")
		return false
	}
	callerUserID, uerr := workflow.UserIDByIdentity(r.Context(), c.pool, c.identityID)
	mine := uerr == nil && callerUserID != "" && order.OwnerUserID == callerUserID
	if !mine {
		logSecurityEvent(r, eventDocExtractIDOR, "identity", c.identityID, "extraction", ex.ID, "record", recordUUID)
		fail(w, http.StatusNotFound, "Record not found.")
		return false
	}
	return true
}

// auditCreatedFromDocument writes the creation provenance on the sales order's
// own audit trail. Best-effort: a failure is logged, never returned.
func (h *DocExtractOps) auditCreatedFromDocument(r *http.Request, c *docExtractCaller, ex *docextractjob.Extraction, recordUUID string) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, c.pool, c.identityID)
	extra := map[string]any{
		"extraction_id": ex.ID,
		"file_name":     ex.FileName,
		"sha256":        strings.ToLower(ex.SHA256),
		"method":        ex.Method,
		"model":         ex.Model,
	}
	if err := workflow.LogAuditFull(ctx, c.pool, actorUserID, auditActionCreatedFromDocument, "sales_order", recordUUID, "sales_order",
		nil, nil, extra, clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		slog.WarnContext(ctx, "doc extract: audit created_from_document", "extractionId", ex.ID, "error", err)
	}
}
