package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"stonesuite-backend/feedback"
	"stonesuite-backend/middleware"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// FeedbackOps handles reporter-facing feedback ticket endpoints — filing a
// ticket, tracking "My Tickets", replying, and attaching files. The SAME
// handlers are registered under both /api/tenant/feedback* and
// /api/portal/feedback* (see main.go): a portal token is confined by
// RequireAuth to /api/portal/*, and a staff token never carries
// kind="portal", so one implementation safely serves both route trees.
//
// Tickets live in the control-plane DB (see package feedback), not the
// tenant DB — every handler here reads/writes via h.CP.Pool(), never
// tenancy.PoolFromContext. Handlers still run behind the normal
// tenantChain/portalChain (RequireAuth -> resolver.Middleware) purely to get
// tenancy.TenantFromContext for free: the resolved tenant's id/slug/bucket
// and its Servable() gate, shared with every other tenant/portal route.
//
// There is no RBAC resource for feedback: any authenticated staff or portal
// user may file a ticket, and may read/reply to only their own — the store
// layer hard-filters on reporter_identity_id, so there is no scope to
// configure and no IDOR surface to guard beyond that filter (a miss and an
// out-of-scope read both come back as 404, the same convention every other
// record endpoint uses).
type FeedbackOps struct {
	CP *tenancy.ControlPlane
	r2 *storage.Client // nil when R2 is not configured (graceful degradation)
}

// NewFeedbackOps constructs the handler group. r2 may be nil when R2
// credentials are absent; attachment endpoints that require R2 return 503.
func NewFeedbackOps(cp *tenancy.ControlPlane, r2 *storage.Client) *FeedbackOps {
	return &FeedbackOps{CP: cp, r2: r2}
}

func (h *FeedbackOps) r2ForTenant(tenant *tenancy.Tenant) *storage.Client {
	return h.r2.WithBucket(tenant.R2Bucket)
}

const (
	// maxFeedbackAttachments caps files per ticket lower than the general
	// record-attachment batch cap (maxFilesPerBatch=10) — bug reports rarely
	// need more than a handful of screenshots/logs.
	maxFeedbackAttachments = 5
	maxPageURLLength       = 2048
	maxUserAgentLength     = 512
)

// reporterPrincipal resolves the calling identity and derives their reporter
// kind (feedback.KindPortal for a customer-portal token, feedback.KindStaff
// otherwise) from the same JWT claim RequirePortal/RequireAuth already
// validated — it does not re-derive trust, only reads what middleware set.
func reporterPrincipal(r *http.Request) (payload middleware.UserContextPayload, kind string, ok bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		return payload, "", false
	}
	kind = feedback.KindStaff
	if payload.Kind == middleware.KindPortal {
		kind = feedback.KindPortal
	}
	return payload, kind, true
}

// reporterDisplayName resolves the identity's full name (falling back to the
// email local-part) for the reporter_name / author_name snapshot.
func reporterDisplayName(identity *tenancy.Identity) string {
	if identity.FullName != "" {
		return identity.FullName
	}
	if at := strings.IndexByte(identity.Email, '@'); at > 0 {
		return identity.Email[:at]
	}
	return identity.Email
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// ---- POST /api/{tenant,portal}/feedback --------------------------------------

type submitFeedbackRequest struct {
	Category    string `json:"category"`
	Area        string `json:"area,omitempty"`
	Rating      *int   `json:"rating,omitempty"`
	Description string `json:"description"`
	PageURL     string `json:"pageUrl,omitempty"`
}

// Submit files a new feedback ticket for the calling staff or portal user.
// page_url is trusted as display-only context (never used in a query or
// path); user_agent is read from the actual request header, not the body,
// so it reflects the real client rather than whatever a caller claims.
func (h *FeedbackOps) Submit(w http.ResponseWriter, r *http.Request) {
	payload, kind, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	var req submitFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	identity, err := h.CP.IdentityByID(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve reporter identity.")
		return
	}

	in := feedback.CreateInput{
		TenantID:           tenant.ID,
		ReporterIdentityID: payload.ID,
		ReporterKind:       kind,
		ReporterEmail:      identity.Email,
		ReporterName:       reporterDisplayName(identity),
		Category:           req.Category,
		Area:               req.Area,
		Rating:             req.Rating,
		Description:        req.Description,
		PageURL:            truncate(req.PageURL, maxPageURLLength),
		UserAgent:          truncate(r.UserAgent(), maxUserAgentLength),
	}
	if err := feedback.ValidateCreate(in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	ticket, err := feedback.Create(r.Context(), h.CP.Pool(), in)
	if err != nil {
		log.Printf("feedback.Create: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to submit feedback.")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "ticket": ticket})
}

// ---- GET /api/{tenant,portal}/feedback ---------------------------------------

// ListMine returns the caller's own tickets, newest first, keyset paginated.
func (h *FeedbackOps) ListMine(w http.ResponseWriter, r *http.Request) {
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	tickets, next, err := feedback.ListForReporter(r.Context(), h.CP.Pool(), tenant.ID, payload.ID, r.URL.Query().Get("cursor"), limit)
	if errors.Is(err, feedback.ErrInvalidCursor) {
		fail(w, http.StatusBadRequest, "Invalid pagination cursor.")
		return
	}
	if err != nil {
		log.Printf("feedback.ListForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to list feedback tickets.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tickets": tickets, "nextCursor": next})
}

// ---- GET /api/{tenant,portal}/feedback/{id} ----------------------------------

// Get returns one of the caller's own tickets with its reporter-visible
// timeline and attachments. A ticket that exists but belongs to someone else
// comes back 404 — same as a real miss — so ids cannot be enumerated.
func (h *FeedbackOps) Get(w http.ResponseWriter, r *http.Request) {
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	id := r.PathValue("id")
	ticket, err := feedback.GetForReporter(r.Context(), h.CP.Pool(), id, tenant.ID, payload.ID)
	if errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	}
	if err != nil {
		log.Printf("feedback.GetForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	comments, err := feedback.ListComments(r.Context(), h.CP.Pool(), id, false)
	if err != nil {
		log.Printf("feedback.ListComments: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}
	attachments, err := feedback.ListAttachments(r.Context(), h.CP.Pool(), id)
	if err != nil {
		log.Printf("feedback.ListAttachments: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "ticket": ticket, "comments": comments, "attachments": attachments,
	})
}

// ---- POST /api/{tenant,portal}/feedback/{id}/comments ------------------------

type addFeedbackCommentRequest struct {
	Body string `json:"body"`
}

// AddComment appends the reporter's reply to their own ticket's timeline.
func (h *FeedbackOps) AddComment(w http.ResponseWriter, r *http.Request) {
	payload, kind, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	id := r.PathValue("id")

	// Ownership check before touching the timeline — 404s the same as a miss.
	if _, err := feedback.GetForReporter(r.Context(), h.CP.Pool(), id, tenant.ID, payload.ID); errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	} else if err != nil {
		log.Printf("feedback.GetForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	var req addFeedbackCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := feedback.ValidateComment(req.Body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	identity, err := h.CP.IdentityByID(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve reporter identity.")
		return
	}

	comment, err := feedback.AddComment(r.Context(), h.CP.Pool(), feedback.AddCommentInput{
		FeedbackID: id, AuthorIdentityID: payload.ID, AuthorKind: kind,
		AuthorName: reporterDisplayName(identity), Body: req.Body, IsInternal: false,
	})
	if err != nil {
		log.Printf("feedback.AddComment: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to add reply.")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "comment": comment})
}

// ---- GET /api/{tenant,portal}/feedback/unread-count --------------------------

// UnreadCount returns how many of the caller's own tickets have an
// admin-side reply or status change since their last visit, for the
// notification-dot badge next to the feedback icon.
func (h *FeedbackOps) UnreadCount(w http.ResponseWriter, r *http.Request) {
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	count, err := feedback.UnreadCountForReporter(r.Context(), h.CP.Pool(), tenant.ID, payload.ID)
	if err != nil {
		log.Printf("feedback.UnreadCountForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load unread count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "unreadCount": count})
}

// ---- POST /api/{tenant,portal}/feedback/mark-seen -----------------------------

// MarkSeen clears the unread badge for every one of the caller's own
// tickets. Called when the "My Tickets" tab opens, rather than tracked
// per-ticket — the panel shows the whole list at once anyway.
func (h *FeedbackOps) MarkSeen(w http.ResponseWriter, r *http.Request) {
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	if err := feedback.MarkAllSeenForReporter(r.Context(), h.CP.Pool(), tenant.ID, payload.ID); err != nil {
		log.Printf("feedback.MarkAllSeenForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to update.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ---- POST /api/{tenant,portal}/feedback/{id}/attachments/presign -------------

// PresignAttachments validates incoming file metadata and returns one
// presigned PUT URL per file, scoped under this ticket's own storage-key
// prefix in the reporting tenant's R2 bucket.
func (h *FeedbackOps) PresignAttachments(w http.ResponseWriter, r *http.Request) {
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}
	id := r.PathValue("id")
	if _, err := feedback.GetForReporter(r.Context(), h.CP.Pool(), id, tenant.ID, payload.ID); errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	} else if err != nil {
		log.Printf("feedback.GetForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	var req presignBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(req.Files) == 0 {
		fail(w, http.StatusBadRequest, "At least one file is required.")
		return
	}
	if len(req.Files) > maxFeedbackAttachments {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Maximum %d files per ticket.", maxFeedbackAttachments))
		return
	}
	for i, f := range req.Files {
		if err := validateAttachFile(f.FileName, f.ContentType, f.SizeBytes); err != nil {
			fail(w, http.StatusBadRequest, fmt.Sprintf("File %d: %s", i+1, err.Error()))
			return
		}
	}

	out := make([]presignFileOut, 0, len(req.Files))
	for _, f := range req.Files {
		safe := workflow.SanitizeFileName(f.FileName)
		attachUUID, uErr := newAttachUUID()
		if uErr != nil {
			fail(w, http.StatusInternalServerError, "Failed to generate file key.")
			return
		}
		storageKey := workflow.GenerateStorageKey(tenant.Slug, "feedback", id, attachUUID, safe)
		uploadURL, pErr := h.r2ForTenant(tenant).PresignPut(r.Context(), storageKey, f.ContentType, presignPutTTL)
		if pErr != nil {
			fail(w, http.StatusInternalServerError, "Failed to generate upload URL.")
			return
		}
		out = append(out, presignFileOut{FileName: safe, StorageKey: storageKey, UploadURL: uploadURL})
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "files": out})
}

// ---- POST /api/{tenant,portal}/feedback/{id}/attachments ---------------------

// ConfirmAttachments inserts attachment metadata rows after the client has
// finished uploading to R2.
func (h *FeedbackOps) ConfirmAttachments(w http.ResponseWriter, r *http.Request) {
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	id := r.PathValue("id")
	if _, err := feedback.GetForReporter(r.Context(), h.CP.Pool(), id, tenant.ID, payload.ID); errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	} else if err != nil {
		log.Printf("feedback.GetForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
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
	if len(req.Attachments) > maxFeedbackAttachments {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Maximum %d attachments per ticket.", maxFeedbackAttachments))
		return
	}

	// The storage key must fall under THIS ticket's own prefix — stronger
	// than the generic tenant-prefix check on record attachments, since here
	// we already know the exact ticket, not just the tenant.
	expectedPrefix := tenant.Slug + "/feedback/" + id + "/"

	inserted := make([]feedback.Attachment, 0, len(req.Attachments))
	for i, a := range req.Attachments {
		if a.StorageKey == "" || !strings.HasPrefix(a.StorageKey, expectedPrefix) {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Attachment %d: invalid storageKey.", i+1))
			return
		}
		safe := workflow.SanitizeFileName(a.FileName)
		att, insErr := feedback.InsertAttachment(r.Context(), h.CP.Pool(), feedback.Attachment{
			FeedbackID: id, FileName: safe, ContentType: a.ContentType,
			SizeBytes: a.SizeBytes, StorageKey: a.StorageKey, ChecksumSHA256: a.ChecksumSHA256,
		})
		if insErr != nil {
			log.Printf("feedback.InsertAttachment: %v", insErr)
			fail(w, http.StatusInternalServerError, "Failed to save attachment metadata.")
			return
		}
		inserted = append(inserted, *att)
	}

	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "attachments": inserted})
}

// ---- GET /api/{tenant,portal}/feedback/{id}/attachments/{attachmentId}/download ----

// DownloadAttachment generates a short-lived presigned GET URL.
func (h *FeedbackOps) DownloadAttachment(w http.ResponseWriter, r *http.Request) {
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	payload, _, ok := reporterPrincipal(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}
	id := r.PathValue("id")
	if _, err := feedback.GetForReporter(r.Context(), h.CP.Pool(), id, tenant.ID, payload.ID); errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	} else if err != nil {
		log.Printf("feedback.GetForReporter: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	attachmentID := r.PathValue("attachmentId")
	att, err := feedback.GetAttachment(r.Context(), h.CP.Pool(), id, attachmentID)
	if errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Attachment not found.")
		return
	}
	if err != nil {
		log.Printf("feedback.GetAttachment: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load attachment.")
		return
	}

	downloadURL, err := h.r2ForTenant(tenant).PresignGet(r.Context(), att.StorageKey, presignGetTTL)
	if err != nil {
		log.Printf("feedback attachment PresignGet: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to generate download URL.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "downloadUrl": downloadURL, "fileName": att.FileName,
		"expiresIn": int(presignGetTTL.Seconds()),
	})
}
