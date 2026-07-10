package controllers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// AttachmentOps handles generic record-attachment endpoints. Attachments are
// keyed off workflow_records(id) and stored in Cloudflare R2 — following the
// same "one mechanism, generic via record_id" pattern as workflow_record_history
// and workflow_numbering_configs.
//
// Routes (all behind RequireAuth + tenancy resolver):
//
//	POST   /api/tenant/records/{id}/attachments/presign-batch
//	POST   /api/tenant/records/{id}/attachments
//	GET    /api/tenant/records/{id}/attachments
//	GET    /api/tenant/records/{id}/attachments/{attachmentId}/download
//	DELETE /api/tenant/records/{id}/attachments/{attachmentId}
type AttachmentOps struct {
	r2 *storage.Client // nil when R2 is not configured (graceful degradation)
}

// NewAttachmentOps constructs the handler group. r2 may be nil when R2
// credentials are absent; attachment endpoints that require R2 return 503.
func NewAttachmentOps(r2 *storage.Client) *AttachmentOps {
	return &AttachmentOps{r2: r2}
}

// r2ForTenant returns an R2 client scoped to the tenant's dedicated bucket.
// Returns nil (treated as "not configured" / HTTP 503) when no bucket has
// been assigned — every tenant must have r2_bucket set via provisioning.
func (h *AttachmentOps) r2ForTenant(tenant *tenancy.Tenant) *storage.Client {
	return h.r2.WithBucket(tenant.R2Bucket)
}

// ---- validation constants ---------------------------------------------------

const (
	maxFilesPerBatch = 10
	maxFileSizeBytes = 25 * 1024 * 1024  // 25 MB per file
	maxRecordBytes   = 200 * 1024 * 1024 // 200 MB cumulative per record
	presignPutTTL    = 5 * time.Minute
	presignGetTTL    = 60 * time.Second
)

// allowedMIME maps accepted content-types to their canonical extension.
var allowedMIME = map[string]string{
	"application/pdf": ".pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       ".xlsx",
	"image/png":  ".png",
	"image/jpeg": ".jpg",
}

// allowedExt maps accepted extensions to their canonical content-type.
var allowedExt = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
}

// ---- authorisation ----------------------------------------------------------

// attachAuth is the RBAC + IDOR gate for attachment handlers. It resolves the
// record's actual type — regardless of which of the three storage models
// backs it (v1 workflow_records, v2 relational customer/CRM, or the
// relational sales_order) — and checks the SPECIFIC resource:action
// permission for that type, not any of a fixed list: a caller with only
// sales_order:read must not be able to read attachments on a lead just
// because "any CRM-ish permission" used to be accepted. When the caller's
// grant is scoped to own/team, it also enforces the same row-level ownership
// guard every other record endpoint uses (recordInScope). Denial is always
// 404 (never 403), so callers cannot enumerate ids outside their scope or
// permission set. Returns pool, the resolved record info, identityID, ok.
func (h *AttachmentOps) attachAuth(
	w http.ResponseWriter, r *http.Request, recordID string, action authz.Action,
) (*pgxpool.Pool, workflow.RecordAccessInfo, string, bool) {

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}

	info, err := workflow.ResolveRecordAccess(r.Context(), pool, recordID)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}

	resource := resourceForKey(info.WorkflowKey)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to access attachments.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	if decision.Scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, payload.ID, info.OwnerUserID, info.TeamID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, workflow.RecordAccessInfo{}, "", false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", payload.ID, "record", recordID, "resource", string(resource),
				"action", string(action), "scope", string(decision.Scope))
			fail(w, http.StatusNotFound, "Record not found.")
			return nil, workflow.RecordAccessInfo{}, "", false
		}
	}
	return pool, info, payload.ID, true
}

// ---- POST /api/tenant/records/{id}/attachments/presign-batch ----------------

type presignFileIn struct {
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type presignBatchRequest struct {
	Files []presignFileIn `json:"files"`
}

type presignFileOut struct {
	FileName   string `json:"fileName"`   // sanitized display name
	StorageKey string `json:"storageKey"` // echo back in confirm
	UploadURL  string `json:"uploadUrl"`  // presigned PUT URL (TTL 5 min)
}

// PresignBatch validates incoming file metadata and returns one presigned PUT
// URL per file. RBAC: record:update.
func (h *AttachmentOps) PresignBatch(w http.ResponseWriter, r *http.Request) {
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	recordID := r.PathValue("id")
	pool, info, _, ok := h.attachAuth(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	workflowKey := info.WorkflowKey

	var req presignBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(req.Files) == 0 {
		fail(w, http.StatusBadRequest, "At least one file is required.")
		return
	}
	if len(req.Files) > maxFilesPerBatch {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Maximum %d files per batch.", maxFilesPerBatch))
		return
	}

	// Get tenant — its r2_bucket must be set (provisioned at onboarding).
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

	// Validate individual files (content-type, extension, per-file size).
	for i, f := range req.Files {
		if err := validateAttachFile(f.FileName, f.ContentType, f.SizeBytes); err != nil {
			fail(w, http.StatusBadRequest, fmt.Sprintf("File %d: %s", i+1, err.Error()))
			return
		}
	}

	// Check per-record cumulative size cap.
	existing, err := workflow.CurrentAttachmentBytes(r.Context(), pool, recordID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check attachment size.")
		return
	}
	var incoming int64
	for _, f := range req.Files {
		incoming += f.SizeBytes
	}
	if existing+incoming > maxRecordBytes {
		fail(w, http.StatusBadRequest,
			fmt.Sprintf("Upload would exceed the %d MB per-record storage limit.", maxRecordBytes/1024/1024))
		return
	}

	// Generate presigned URLs.
	out := make([]presignFileOut, 0, len(req.Files))
	for _, f := range req.Files {
		safe := workflow.SanitizeFileName(f.FileName)
		attachUUID, uErr := newAttachUUID()
		if uErr != nil {
			fail(w, http.StatusInternalServerError, "Failed to generate file key.")
			return
		}
		storageKey := workflow.GenerateStorageKey(
			tenant.Slug, workflowKey, recordID, attachUUID, safe,
		)
		uploadURL, pErr := h.r2ForTenant(tenant).PresignPut(r.Context(), storageKey, f.ContentType, presignPutTTL)
		if pErr != nil {
			fail(w, http.StatusInternalServerError, "Failed to generate upload URL.")
			return
		}
		out = append(out, presignFileOut{
			FileName:   safe,
			StorageKey: storageKey,
			UploadURL:  uploadURL,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "files": out})
}

// ---- POST /api/tenant/records/{id}/attachments ------------------------------

type confirmAttachIn struct {
	FileName       string `json:"fileName"`
	ContentType    string `json:"contentType"`
	SizeBytes      int64  `json:"sizeBytes"`
	StorageKey     string `json:"storageKey"`
	ChecksumSHA256 string `json:"checksumSha256"`
}

type confirmBatchRequest struct {
	Attachments []confirmAttachIn `json:"attachments"`
}

// ConfirmAttachments inserts attachment metadata rows after the client has
// finished uploading to R2. RBAC: record:update.
// For v1, status is set immediately to 'clean'; see workflow.InsertAttachment
// for the malware-scanning extension point comment.
func (h *AttachmentOps) ConfirmAttachments(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, identityID, ok := h.attachAuth(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}

	var req confirmBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(req.Attachments) == 0 {
		fail(w, http.StatusBadRequest, "At least one attachment is required.")
		return
	}
	if len(req.Attachments) > maxFilesPerBatch {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Maximum %d attachments per call.", maxFilesPerBatch))
		return
	}

	// Resolve actor user ID (for uploaded_by and audit log).
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)

	// Tenant slug: validate that storageKey prefix matches this tenant to
	// prevent a tenant from claiming an object uploaded by another tenant.
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	tenantPrefix := tenant.Slug + "/"

	inserted := make([]workflow.Attachment, 0, len(req.Attachments))
	for i, a := range req.Attachments {
		if a.StorageKey == "" {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Attachment %d: storageKey is required.", i+1))
			return
		}
		if !strings.HasPrefix(a.StorageKey, tenantPrefix) {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Attachment %d: invalid storageKey.", i+1))
			return
		}
		safe := workflow.SanitizeFileName(a.FileName)
		id, insErr := workflow.InsertAttachment(r.Context(), pool, workflow.Attachment{
			RecordID:         recordID,
			FileName:         safe,
			ContentType:      a.ContentType,
			SizeBytes:        a.SizeBytes,
			StorageKey:       a.StorageKey,
			ChecksumSHA256:   a.ChecksumSHA256,
			UploadedByUserID: actorUserID,
		})
		if insErr != nil {
			fail(w, http.StatusInternalServerError, "Failed to save attachment metadata.")
			return
		}
		inserted = append(inserted, workflow.Attachment{
			ID:         id,
			RecordID:   recordID,
			FileName:   safe,
			Status:     "clean",
			StorageKey: a.StorageKey,
			SizeBytes:  a.SizeBytes,
		})

		// Audit log: upload confirmed (best-effort).
		if logErr := workflow.LogAudit(r.Context(), pool, actorUserID,
			"attachment.upload", "record_attachment", id,
			map[string]any{"recordId": recordID, "fileName": safe, "sizeBytes": a.SizeBytes},
		); logErr != nil {
			log.Printf("audit log (attachment.upload) attachment=%s: %v", id, logErr)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":     true,
		"attachments": inserted,
	})
}

// ---- GET /api/tenant/records/{id}/attachments --------------------------------

// ListAttachments returns all attachments for a record. RBAC: record:read.
func (h *AttachmentOps) ListAttachments(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, _, ok := h.attachAuth(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}

	attachments, err := workflow.ListAttachments(r.Context(), pool, recordID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list attachments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"attachments": attachments,
	})
}

// ---- GET /api/tenant/records/{id}/attachments/{attachmentId}/download --------

// DownloadAttachment generates a short-lived presigned GET URL. RBAC: record:read.
// The URL carries Content-Disposition: attachment so browsers download rather
// than render uploaded HTML/SVG inline (XSS prevention).
func (h *AttachmentOps) DownloadAttachment(w http.ResponseWriter, r *http.Request) {
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	recordID := r.PathValue("id")
	pool, _, identityID, ok := h.attachAuth(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	attachmentID := r.PathValue("attachmentId")

	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

	att, err := workflow.GetAttachment(r.Context(), pool, recordID, attachmentID)
	if errors.Is(err, workflow.ErrAttachmentNotFound) {
		fail(w, http.StatusNotFound, "Attachment not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load attachment.")
		return
	}

	downloadURL, err := h.r2ForTenant(tenant).PresignGet(r.Context(), att.StorageKey, presignGetTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate download URL.")
		return
	}

	// Audit log: download (best-effort).
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	if logErr := workflow.LogAudit(r.Context(), pool, actorUserID,
		"attachment.download", "record_attachment", attachmentID,
		map[string]any{"recordId": recordID, "fileName": att.FileName},
	); logErr != nil {
		log.Printf("audit log (attachment.download) attachment=%s: %v", attachmentID, logErr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"downloadUrl": downloadURL,
		"fileName":    att.FileName,
		"expiresIn":   int(presignGetTTL.Seconds()),
	})
}

// ---- DELETE /api/tenant/records/{id}/attachments/{attachmentId} --------------

// DeleteAttachment removes the R2 object (best-effort) and the metadata row.
// RBAC: record:update. If R2 deletion fails the row is still removed and the
// error is logged — the UI must not be left with an inaccessible phantom entry.
func (h *AttachmentOps) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, identityID, ok := h.attachAuth(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	attachmentID := r.PathValue("attachmentId")

	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

	att, err := workflow.GetAttachment(r.Context(), pool, recordID, attachmentID)
	if errors.Is(err, workflow.ErrAttachmentNotFound) {
		fail(w, http.StatusNotFound, "Attachment not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load attachment.")
		return
	}

	// Best-effort R2 object deletion. If it fails we log the storage key so
	// an operator can clean it up manually, then remove the metadata row.
	if h.r2.IsConfigured() {
		if r2Err := h.r2ForTenant(tenant).Delete(r.Context(), att.StorageKey); r2Err != nil {
			log.Printf("WARNING: R2 delete failed for key %q (attachment %s): %v — removing metadata row anyway",
				att.StorageKey, attachmentID, r2Err)
		}
	}

	if err := workflow.DeleteAttachmentRow(r.Context(), pool, recordID, attachmentID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to remove attachment record.")
		return
	}

	// Audit log: delete (best-effort).
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	if logErr := workflow.LogAudit(r.Context(), pool, actorUserID,
		"attachment.delete", "record_attachment", attachmentID,
		map[string]any{"recordId": recordID, "fileName": att.FileName, "storageKey": att.StorageKey},
	); logErr != nil {
		log.Printf("audit log (attachment.delete) attachment=%s: %v", attachmentID, logErr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Attachment deleted.",
	})
}

// ---- local helpers ----------------------------------------------------------

// validateAttachFile checks that a single file entry is on the content-type
// allowlist, that its extension matches the content-type, and that it is
// within the per-file size cap.
func validateAttachFile(fileName, contentType string, sizeBytes int64) error {
	if _, ok := allowedMIME[contentType]; !ok {
		return fmt.Errorf("content type %q is not allowed (allowed: pdf, docx, xlsx, png, jpg)", contentType)
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	expectedCT, ok := allowedExt[ext]
	if !ok {
		return fmt.Errorf("extension %q is not allowed", ext)
	}
	if expectedCT != contentType {
		return fmt.Errorf("content type %q does not match file extension %q", contentType, ext)
	}
	if sizeBytes <= 0 {
		return fmt.Errorf("sizeBytes must be > 0")
	}
	if sizeBytes > maxFileSizeBytes {
		return fmt.Errorf("file exceeds the %d MB per-file limit", maxFileSizeBytes/1024/1024)
	}
	return nil
}

// newAttachUUID generates a random UUID v4 (crypto/rand) for use in storage keys.
func newAttachUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Compile-time check: context must be importable from this package.
var _ = context.Background
