package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/recordemail"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

const (
	// replyRequestOverheadBytes is what a reply request may carry beyond its files:
	// the message text (at most maxReplyChars characters) and the multipart framing.
	replyRequestOverheadBytes = 1 << 20
	// replyMultipartMemory is how much of a multipart reply is parsed in memory
	// before spilling to disk. It is the size of the largest accepted request, so a
	// valid reply never touches the disk.
	replyMultipartMemory = recordemail.ReplyMaxTotalBytes + replyRequestOverheadBytes

	defaultAttachmentContentType = "application/octet-stream"
	maxAttachmentContentType     = 100

	noStorageMessage = "File storage is not set up for this workspace."
)

// ObjectStore is the part of the R2 client the Communication tab's files need.
type ObjectStore interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
}

// TenantStorage returns the object store of a tenant's own bucket, or nil when the
// tenant has none, which is how "file storage is not set up" reads here.
type TenantStorage func(tenant *tenancy.Tenant) ObjectStore

// R2TenantStorage adapts the R2 client (nil when R2 is not configured) to a
// per-tenant bucket lookup.
func R2TenantStorage(r2 *storage.Client) TenantStorage {
	return func(tenant *tenancy.Tenant) ObjectStore {
		// Not `return r2.WithBucket(...)`: a nil *storage.Client in an interface is
		// not a nil interface.
		if c := r2.WithBucket(tenant.R2Bucket); c != nil {
			return c
		}
		return nil
	}
}

// WithStorage lets the Communication tab keep files (received ones, and the ones
// staff attach) and returns h. Without it a thread still works: files received
// are listed as unavailable and the reply box cannot attach any.
func (h *RecordEmailOps) WithStorage(s TenantStorage) *RecordEmailOps {
	h.storageFor = s
	return h
}

// storeFor returns the tenant's object store, nil when it cannot keep files.
func (h *RecordEmailOps) storeFor(tenant *tenancy.Tenant) ObjectStore {
	if h == nil || h.storageFor == nil || tenant == nil {
		return nil
	}
	return h.storageFor(tenant)
}

// cleanContentType returns the first candidate that is a plain "type/subtype",
// else the generic binary type. The result goes into a signed storage header, so
// a provider-supplied value is never used as it came.
func cleanContentType(candidates ...string) string {
	for _, c := range candidates {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(c))
		if err == nil && strings.Contains(mediaType, "/") && len(mediaType) <= maxAttachmentContentType {
			return strings.ToLower(mediaType)
		}
	}
	return defaultAttachmentContentType
}

// readBoundedBody reads a request body of at most limit bytes. When the length is
// declared it allocates exactly that once, rather than growing a buffer, since a
// file is read whole on a small machine.
func readBoundedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if n := r.ContentLength; n > limit {
		return nil, &http.MaxBytesError{Limit: limit}
	} else if n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(http.MaxBytesReader(w, r.Body, limit), buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}

// InboundAttachment receives one file of a customer's reply from
// stonesuite-notify (PUT /api/internal/email/inbound/{ref}/attachments/{id}) and
// copies it into the tenant's bucket. ref is the reply address's token and id the
// mail provider's id of the file, both from the Inbound response; they are in the
// path so the signature covers them. Like Inbound, the HMAC signature is the only
// authentication.
//
// 200 means the file reached a final state (stored, or deliberately not kept):
// notify must not retry it. A 5xx means a retry could succeed.
func (h *RecordEmailOps) InboundAttachment(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() || h.secret == "" || h.tenants == nil || h.poolFor == nil || h.storageFor == nil {
		fail(w, http.StatusServiceUnavailable, "Inbound email is not configured.")
		return
	}
	sig := r.Header.Get(middleware.InternalSignatureHeader)
	stamp := r.Header.Get(middleware.InternalTimestampHeader)
	if sig == "" || stamp == "" {
		// Before reading a file's worth of body.
		logSecurityEvent(r, "inbound_attachment_unsigned")
		fail(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}

	body, err := readBoundedBody(w, r, recordemail.InboundMaxFileBytes)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		fail(w, http.StatusRequestEntityTooLarge, "Request body too large.")
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "Could not read the file.")
		return
	}
	if err := middleware.VerifyInternalSignature(h.secret, r.Method, r.URL.Path, stamp, sig, body, h.now()); err != nil {
		logSecurityEvent(r, "inbound_attachment_bad_signature", "reason", err.Error())
		fail(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	if !h.replays.FirstUse(sig, h.now()) {
		logSecurityEvent(r, "inbound_attachment_replay")
		fail(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}

	status, msg := h.storeReceivedFile(r.Context(), r.PathValue("ref"), r.PathValue("attachmentId"),
		r.Header.Get("Content-Type"), body)
	if status != http.StatusOK {
		fail(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// storeReceivedFile copies one received file into the record's conversation. It
// returns http.StatusOK both when the file was kept and when it is not ours to
// keep, and a 5xx when a retry could succeed (see InboundAttachment).
func (h *RecordEmailOps) storeReceivedFile(
	ctx context.Context, ref, providerID, contentType string, data []byte,
) (int, string) {
	target, err := h.addrs.Resolve(ctx, ref)
	if errors.Is(err, recordemail.ErrAddressNotFound) {
		slog.InfoContext(ctx, "inbound attachment: unknown reply address; ignored", "attachment", providerID)
		return http.StatusOK, ""
	}
	if err != nil {
		slog.ErrorContext(ctx, "inbound attachment: resolve address failed", "attachment", providerID, "error", err)
		return http.StatusInternalServerError, "Failed to resolve the reply address."
	}
	tenant, err := h.tenants.TenantByID(ctx, target.TenantID)
	if err != nil || tenant.Status != tenancy.StatusActive {
		slog.InfoContext(ctx, "inbound attachment: tenant unavailable; ignored", "attachment", providerID, "tenant", target.TenantID)
		return http.StatusOK, ""
	}
	pool, err := h.poolFor(ctx, tenant)
	if err != nil {
		slog.ErrorContext(ctx, "inbound attachment: tenant pool unavailable", "tenant", target.TenantID, "error", err)
		return http.StatusInternalServerError, "Failed to open the workspace."
	}

	att, err := recordemail.ReceivedAttachment(ctx, pool, target.RecordID, providerID)
	if errors.Is(err, recordemail.ErrAttachmentNotFound) {
		slog.InfoContext(ctx, "inbound attachment: no such file on the record; ignored",
			"attachment", providerID, "record", target.RecordID)
		return http.StatusOK, ""
	}
	if err != nil {
		slog.ErrorContext(ctx, "inbound attachment: lookup failed", "attachment", providerID, "error", err)
		return http.StatusInternalServerError, "Failed to look up the attachment."
	}
	if att.Status != recordemail.AttachmentPending {
		return http.StatusOK, "" // a retry of a file already settled
	}

	size := int64(len(data))
	settle := func(status string) (int, string) {
		if err := recordemail.SetStatus(ctx, pool, att.ID, status); err != nil {
			slog.ErrorContext(ctx, "inbound attachment: settle failed", "attachment", att.ID, "error", err)
			return http.StatusInternalServerError, "Failed to save the attachment."
		}
		return http.StatusOK, ""
	}
	store := h.storeFor(tenant)
	switch {
	case size > recordemail.InboundMaxFileBytes:
		return settle(recordemail.AttachmentTooLarge)
	case recordemail.IsBlockedFile(att.FileName):
		return settle(recordemail.AttachmentBlocked)
	case store == nil:
		return settle(recordemail.AttachmentUnavailable)
	}
	used, err := recordemail.StoredBytes(ctx, pool, target.RecordID)
	if err != nil {
		slog.ErrorContext(ctx, "inbound attachment: size check failed", "record", target.RecordID, "error", err)
		return http.StatusInternalServerError, "Failed to check the storage used."
	}
	if used+size > recordemail.MaxRecordAttachmentBytes {
		return settle(recordemail.AttachmentUnavailable)
	}

	objectID, err := newAttachUUID()
	if err != nil {
		return http.StatusInternalServerError, "Failed to generate a file key."
	}
	key := workflow.GenerateStorageKey(tenant.Slug, target.WorkflowKey, target.RecordID, objectID, att.FileName)
	ct := cleanContentType(contentType, att.ContentType)
	if err := store.Put(ctx, key, ct, data); err != nil {
		slog.ErrorContext(ctx, "inbound attachment: storing failed", "attachment", att.ID, "error", err)
		return http.StatusBadGateway, "Failed to store the attachment."
	}
	kept, err := recordemail.MarkStored(ctx, pool, att.ID, key, ct, size)
	if err != nil || !kept {
		// Nothing points at the object now (the row failed to update, or another
		// delivery settled the file first): do not leave it behind.
		if delErr := store.Delete(ctx, key); delErr != nil {
			slog.WarnContext(ctx, "inbound attachment: orphan not removed", "key", key, "error", delErr)
		}
		if err != nil {
			slog.ErrorContext(ctx, "inbound attachment: mark stored failed", "attachment", att.ID, "error", err)
			return http.StatusInternalServerError, "Failed to save the attachment."
		}
	}
	return http.StatusOK, ""
}

// DownloadAttachment returns a short-lived link to one file of a record's
// conversation: GET /api/tenant/records/{id}/emails/attachments/{attachmentId}/download.
// The link forces a download rather than rendering the file in the browser (see
// storage.Client.PresignGet). RBAC: <type>:read, scoped like the record itself;
// the file must belong to that record, so a guessed id from another record is a 404.
func (h *RecordEmailOps) DownloadAttachment(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, info, identityID, ok := authRecordAccess(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	if _, ok := threadKinds[info.WorkflowKey]; !ok {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	store := h.storeFor(tenant)
	if store == nil {
		fail(w, http.StatusServiceUnavailable, noStorageMessage)
		return
	}

	ctx := r.Context()
	att, err := recordemail.GetAttachment(ctx, pool, recordID, r.PathValue("attachmentId"))
	if errors.Is(err, recordemail.ErrAttachmentNotFound) {
		fail(w, http.StatusNotFound, "Attachment not found.")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "record email: attachment lookup failed", "record", recordID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load the attachment.")
		return
	}
	if att.Status != recordemail.AttachmentStored || att.StorageKey == "" {
		fail(w, http.StatusConflict, "This file was not kept.")
		return
	}
	url, err := store.PresignGet(ctx, att.StorageKey, presignGetTTL)
	if err != nil {
		slog.ErrorContext(ctx, "record email: download link failed", "attachment", att.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to generate the download link.")
		return
	}

	actor := loadActor(ctx, pool, identityID)
	_ = workflow.LogAudit(ctx, pool, actor.ID, "record_email.attachment_download", "record_email_attachment", att.ID,
		map[string]any{"recordId": recordID, "fileName": att.FileName})

	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"downloadUrl": url,
		"fileName":    att.FileName,
		"expiresIn":   int(presignGetTTL.Seconds()),
	})
}

// replyInput is a Communication-tab message as it arrived: its text, whether it
// is a note, and the files attached to it.
type replyInput struct {
	req   replyRequest
	files []recordemail.ReplyFile
}

// readReply reads a reply sent either as JSON (no files) or as multipart/form-data
// (`message`, `internalOnly` and any number of `files`). A non-zero status is the
// HTTP status to answer with, and problem its message.
func readReply(w http.ResponseWriter, r *http.Request) (in replyInput, status int, problem string) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		if err := json.NewDecoder(r.Body).Decode(&in.req); err != nil {
			return replyInput{}, http.StatusBadRequest, "Invalid request body."
		}
		return in, 0, ""
	}

	r.Body = http.MaxBytesReader(w, r.Body, recordemail.ReplyMaxTotalBytes+replyRequestOverheadBytes)
	if err := r.ParseMultipartForm(replyMultipartMemory); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return replyInput{}, http.StatusRequestEntityTooLarge, recordemail.ReplyTooLargeMessage
		}
		return replyInput{}, http.StatusBadRequest, "Invalid request body."
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	in.req = replyRequest{Message: r.FormValue("message"), InternalOnly: r.FormValue("internalOnly") == "true"}
	headers := r.MultipartForm.File["files"]
	if len(headers) > recordemail.ReplyMaxFiles {
		return replyInput{}, http.StatusBadRequest, fmt.Sprintf("Attach at most %d files.", recordemail.ReplyMaxFiles)
	}
	for _, fh := range headers {
		data, err := readFormFile(fh)
		if err != nil {
			return replyInput{}, http.StatusBadRequest, "Could not read an attached file."
		}
		name := recordemail.BoundFileName(workflow.SanitizeFileName(fh.Filename))
		contentType, _ := recordemail.ReplyContentType(name) // an unlisted type is refused by ValidateReplyFiles
		in.files = append(in.files, recordemail.ReplyFile{Name: name, ContentType: contentType, Data: data})
	}
	return in, 0, ""
}

// readFormFile reads one uploaded file, never more than the whole message may hold.
func readFormFile(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, fmt.Errorf("open attached file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, recordemail.ReplyMaxTotalBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read attached file: %w", err)
	}
	return data, nil
}

// replyFilesProblem is the message to show for files that failed validation.
func replyFilesProblem(err error) string {
	var fe *recordemail.ReplyFilesError
	if errors.As(err, &fe) {
		return fe.Msg
	}
	return "The attachments could not be accepted."
}

// keepReplyFiles copies a reply's files into the tenant's bucket, ahead of the
// message that will own them, and returns their rows (not yet inserted) plus a
// discard func that removes them again should the message not be recorded. A
// non-zero status is the HTTP status to answer with.
func (h *RecordEmailOps) keepReplyFiles(
	ctx context.Context, pool *pgxpool.Pool, tenant *tenancy.Tenant, workflowKey, recordID, messageID string,
	files []recordemail.ReplyFile,
) (atts []recordemail.Attachment, discard func(), status int, problem string) {
	discard = func() {}
	if len(files) == 0 {
		return nil, discard, 0, ""
	}
	store := h.storeFor(tenant)
	if store == nil {
		return nil, discard, http.StatusServiceUnavailable, noStorageMessage
	}
	var total int64
	for _, f := range files {
		total += int64(len(f.Data))
	}
	used, err := recordemail.StoredBytes(ctx, pool, recordID)
	if err != nil {
		slog.ErrorContext(ctx, "record email: size check failed", "record", recordID, "error", err)
		return nil, discard, http.StatusInternalServerError, "Failed to check the storage used."
	}
	if used+total > recordemail.MaxRecordAttachmentBytes {
		return nil, discard, http.StatusConflict, "This record has reached its attachment storage limit."
	}

	var keys []string
	discard = func() {
		for _, key := range keys {
			if err := store.Delete(ctx, key); err != nil {
				slog.WarnContext(ctx, "record email: attachment not removed", "key", key, "error", err)
			}
		}
	}
	for _, f := range files {
		objectID, err := newAttachUUID()
		if err != nil {
			discard()
			return nil, func() {}, http.StatusInternalServerError, "Failed to generate a file key."
		}
		key := workflow.GenerateStorageKey(tenant.Slug, workflowKey, recordID, objectID, f.Name)
		if err := store.Put(ctx, key, f.ContentType, f.Data); err != nil {
			slog.ErrorContext(ctx, "record email: attachment upload failed", "record", recordID, "error", err)
			discard()
			return nil, func() {}, http.StatusBadGateway, "Failed to save the attachments."
		}
		keys = append(keys, key)
		atts = append(atts, recordemail.Attachment{
			MessageID: messageID, RecordID: recordID, FileName: f.Name, ContentType: f.ContentType,
			SizeBytes: int64(len(f.Data)), Status: recordemail.AttachmentStored, StorageKey: key,
		})
	}
	return atts, discard, 0, ""
}
