package controllers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

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
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	recordID := r.PathValue("id")
	pool, info, identityID, ok := h.attachAuth(w, r, recordID, authz.ActionUpdate)
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

	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

	// A key must be one presign minted for THIS record. A tenant-only prefix
	// check let a caller with update rights on one record attach (and then
	// download) any object in the bucket: another record's files, imports,
	// feedback screenshots — bypassing that record's ownership scope.
	prefix := workflow.StorageKeyPrefix(tenant.Slug, info.WorkflowKey, recordID)
	keys := make([]string, 0, len(req.Attachments))
	for i, a := range req.Attachments {
		if !attachmentKeyForRecord(a.StorageKey, prefix) {
			logSecurityEvent(r, "attachment_key_rejected", "identity", identityID, "record", recordID)
			fail(w, http.StatusBadRequest, fmt.Sprintf("Attachment %d: invalid storageKey.", i+1))
			return
		}
		keys = append(keys, a.StorageKey)
	}

	objects, err := verifyUploadedObjects(r.Context(), h.r2ForTenant(tenant), keys)
	if err != nil {
		failObjectVerify(w, err)
		return
	}
	var incoming int64
	for i, a := range req.Attachments {
		if vErr := validateAttachFile(a.FileName, storedContentType(objects[i], a.ContentType), objects[i].Size); vErr != nil {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Attachment %d: %s", i+1, vErr.Error()))
			return
		}
		incoming += objects[i].Size
	}
	existing, err := workflow.CurrentAttachmentBytes(r.Context(), pool, recordID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check attachment size.")
		return
	}
	if existing+incoming > maxRecordBytes {
		fail(w, http.StatusBadRequest,
			fmt.Sprintf("Upload would exceed the %d MB per-record storage limit.", maxRecordBytes/1024/1024))
		return
	}

	inserted := make([]workflow.Attachment, 0, len(req.Attachments))
	for i, a := range req.Attachments {
		safe := workflow.SanitizeFileName(a.FileName)
		size, contentType := objects[i].Size, storedContentType(objects[i], a.ContentType)
		id, insErr := workflow.InsertAttachment(r.Context(), pool, workflow.Attachment{
			RecordID:         recordID,
			FileName:         safe,
			ContentType:      contentType,
			SizeBytes:        size,
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
			SizeBytes:  size,
		})

		// Audit log: upload confirmed (best-effort).
		if logErr := workflow.LogAudit(r.Context(), pool, actorUserID,
			"attachment.upload", "record_attachment", id,
			map[string]any{"recordId": recordID, "fileName": safe, "sizeBytes": size},
		); logErr != nil {
			slog.Error("audit log (attachment.upload)", "attachment", id, "error", logErr)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":     true,
		"attachments": inserted,
	})
}
