package controllers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"stonesuite-backend/docextract"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/storage"
	"stonesuite-backend/workflow"
)

// Client-facing messages of the upload endpoints.
const (
	msgUploadMissing = "The upload didn't finish — please retry."
	msgDailyLimit    = "Your workspace has reached today's document limit. Please try again tomorrow."
	msgStorageFull   = "Too many pending uploads — finish or discard some, or try later."
	msgInvalidBody   = "Invalid request body."
	msgNoStorage     = "File storage is not available for your workspace."
)

// decodeDocExtractBody decodes a small JSON body into dst, answering 400 on failure.
func decodeDocExtractBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxDocExtractBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, msgInvalidBody)
		return false
	}
	return true
}

type docExtractCreateRequest struct {
	DocType     docextract.DocType `json:"docType"`
	FileName    string             `json:"fileName"`
	SizeBytes   int64              `json:"sizeBytes"`
	ContentType string             `json:"contentType"`
}

// genericContentTypes are what browsers send when the OS has no type for a
// file (some Windows setups); the extension then decides the type.
var genericContentTypes = map[string]bool{"": true, "application/octet-stream": true}

// validateDocExtractUpload checks the upload's extension, content type and size
// and returns the sanitized file name, lower-case extension (no dot) and the
// content type the upload must be PUT with. A missing or generic content type
// is replaced by the extension's. A non-empty problem is the user-facing 400
// message (worded like the import page's), so it is a plain sentence rather
// than an error to wrap.
func (h *DocExtractOps) validateDocExtractUpload(req docExtractCreateRequest) (name, ext, contentType, problem string) {
	name = workflow.SanitizeFileName(req.FileName)
	dotExt := strings.ToLower(filepath.Ext(name))
	wantCT, ok := docExtractUploads[dotExt]
	if !ok {
		return "", "", "", fmt.Sprintf("File type %q is not supported (allowed: pdf, docx).", dotExt)
	}
	if req.ContentType != wantCT && !genericContentTypes[req.ContentType] {
		return "", "", "", fmt.Sprintf("Content type %q does not match file extension %q.", req.ContentType, dotExt)
	}
	if req.SizeBytes <= 0 || req.SizeBytes > h.cfg.MaxBytes {
		return "", "", "", fmt.Sprintf("File must be between 1 byte and %d MB.", h.cfg.MaxBytes>>20)
	}
	return name, strings.TrimPrefix(dotExt, "."), wantCT, ""
}

// Create handles POST /api/tenant/document-extractions: validates the upload
// metadata, enforces the daily and staging caps, records an awaiting_upload
// extraction and returns a presigned PUT URL for the server-generated staging key.
// RBAC: Require(<docType resource>, create).
func (h *DocExtractOps) Create(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	c, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	// docType lives in the body, so the (size-bounded) body is read before the
	// permission check; nothing is written until requireCreate passes.
	var req docExtractCreateRequest
	if !decodeDocExtractBody(w, r, &req) {
		return
	}
	if !h.requireCreate(w, r, c, req.DocType) || !h.requireAI(w, r, c) {
		return
	}
	name, ext, contentType, problem := h.validateDocExtractUpload(req)
	if problem != "" {
		fail(w, http.StatusBadRequest, problem)
		return
	}
	objs := h.objectsFor(c.tenant)
	if objs == nil {
		fail(w, http.StatusServiceUnavailable, msgNoStorage)
		return
	}
	if !h.withinCaps(w, r, c, req.SizeBytes) {
		return
	}
	ex, err := c.store.Create(r.Context(), docextractjob.CreateParams{
		DocType: string(req.DocType), OwnerIdentityID: c.identityID, FileName: name, ContentType: contentType,
		SizeBytes: req.SizeBytes, TenantSlug: c.tenant.Slug, Extension: ext, TTL: h.stagingTTL(),
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: create extraction", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to start document extraction.")
		return
	}
	url, err := objs.PresignPut(r.Context(), ex.StagingKey, ex.ContentType, h.cfg.PresignTTL)
	if err != nil {
		if _, derr := c.store.Discard(r.Context(), ex.ID, c.identityID); derr != nil {
			slog.WarnContext(r.Context(), "doc extract: discard after presign failure", "extractionId", ex.ID, "error", derr)
		}
		fail(w, http.StatusInternalServerError, "Failed to generate upload URL.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"success": true, "id": ex.ID, "uploadUrl": url, "contentType": ex.ContentType,
		"expiresAt": time.Now().Add(h.cfg.PresignTTL).UTC(),
	})
}

// stagingTTL is how long an extraction row (and its staging object) lives.
func (h *DocExtractOps) stagingTTL() time.Duration { return h.cfg.StagingTTL }

// withinCaps enforces the tenant's daily extraction cap (429) and staging byte
// quota (409), writing the response and returning false when exceeded.
func (h *DocExtractOps) withinCaps(w http.ResponseWriter, r *http.Request, c *docExtractCaller, size int64) bool {
	today, err := c.store.CountCreatedToday(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: count today", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to start document extraction.")
		return false
	}
	if today >= h.cfg.DailyCap {
		failWithCode(w, http.StatusTooManyRequests, msgDailyLimit, codeDailyLimit)
		return false
	}
	pending, err := c.store.SumPendingStagingBytes(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: sum staging bytes", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to start document extraction.")
		return false
	}
	if pending+size > h.cfg.StagingMaxBytes {
		failWithCode(w, http.StatusConflict, msgStorageFull, codeStorageFull)
		return false
	}
	return true
}

// Presign handles POST /api/tenant/document-extractions/{id}/presign: re-issues
// the upload URL for the same id and staging key while awaiting_upload.
// RBAC: Require(<doc type resource>, create); owner-only (404 otherwise).
func (h *DocExtractOps) Presign(w http.ResponseWriter, r *http.Request) {
	c, ex, ok := h.authOwned(w, r, true)
	if !ok {
		return
	}
	if ex.Status != docextractjob.StatusAwaitingUpload {
		failWithCode(w, http.StatusConflict, "This upload can no longer be retried.", codeInvalidState)
		return
	}
	objs := h.objectsFor(c.tenant)
	if objs == nil {
		fail(w, http.StatusServiceUnavailable, msgNoStorage)
		return
	}
	url, err := objs.PresignPut(r.Context(), ex.StagingKey, ex.ContentType, h.cfg.PresignTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate upload URL.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "id": ex.ID, "uploadUrl": url, "contentType": ex.ContentType,
		"expiresAt": time.Now().Add(h.cfg.PresignTTL).UTC(),
	})
}

type docExtractStartRequest struct {
	ClientSHA256 string `json:"clientSha256"`
}

// validSHA256Hex reports whether s is a 64-character hex digest.
func validSHA256Hex(s string) bool {
	if len(s) != sha256HexLen {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Start handles POST /api/tenant/document-extractions/{id}/start: verifies the
// upload landed (Head), then enqueues the extraction job. Idempotent: an
// extraction already queued/running/ready answers 202 with its current status.
// RBAC: Require(<doc type resource>, create); owner-only (404 otherwise).
func (h *DocExtractOps) Start(w http.ResponseWriter, r *http.Request) {
	c, ex, ok := h.authOwned(w, r, true)
	if !ok {
		return
	}
	var req docExtractStartRequest
	if !decodeDocExtractBody(w, r, &req) {
		return
	}
	if !validSHA256Hex(req.ClientSHA256) {
		fail(w, http.StatusBadRequest, "clientSha256 must be a 64-character hex SHA-256.")
		return
	}
	switch ex.Status {
	case docextractjob.StatusQueued, docextractjob.StatusRunning, docextractjob.StatusReady:
		writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "status": ex.Status})
		return
	case docextractjob.StatusAwaitingUpload:
	default:
		failWithCode(w, http.StatusConflict, "This document can no longer be processed.", codeInvalidState)
		return
	}
	if !h.verifyUpload(w, r, c, ex) {
		return
	}
	jobID, err := h.queue.Enqueue(r.Context(), docextractjob.JobTypeDocExtract, c.tenant.ID,
		docextractjob.JobPayload{ExtractionID: ex.ID}, ex.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: enqueue", "extractionId", ex.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to start document extraction.")
		return
	}
	status, _, err := c.store.MarkQueued(r.Context(), ex.ID, c.identityID, strings.ToLower(req.ClientSHA256), jobID)
	if err != nil {
		if errors.Is(err, docextractjob.ErrNotFound) {
			fail(w, http.StatusNotFound, "Document extraction not found.")
			return
		}
		slog.ErrorContext(r.Context(), "doc extract: mark queued", "extractionId", ex.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to start document extraction.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "status": status})
}

// verifyUpload Heads the staging object: missing -> 409 upload_missing, over the
// size cap -> 400 (and the oversized object and row are discarded).
func (h *DocExtractOps) verifyUpload(w http.ResponseWriter, r *http.Request, c *docExtractCaller, ex *docextractjob.Extraction) bool {
	objs := h.objectsFor(c.tenant)
	if objs == nil {
		fail(w, http.StatusServiceUnavailable, msgNoStorage)
		return false
	}
	info, err := objs.Head(r.Context(), ex.StagingKey)
	if errors.Is(err, storage.ErrNotFound) {
		failWithCode(w, http.StatusConflict, msgUploadMissing, codeUploadMiss)
		return false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "doc extract: head staging object", "extractionId", ex.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Could not verify the upload. Please retry.")
		return false
	}
	if info.Size > h.cfg.MaxBytes {
		if _, derr := c.store.Discard(r.Context(), ex.ID, c.identityID); derr != nil {
			slog.WarnContext(r.Context(), "doc extract: discard oversized upload", "extractionId", ex.ID, "error", derr)
		}
		h.deleteStaging(r.Context(), c.tenant, ex)
		fail(w, http.StatusBadRequest, fmt.Sprintf("File must be between 1 byte and %d MB.", h.cfg.MaxBytes>>20))
		return false
	}
	return true
}
