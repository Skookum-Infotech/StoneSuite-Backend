package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"stonesuite-backend/feedback"
	"stonesuite-backend/middleware"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// auditDetails builds the details JSON for a platform_audit_logs row. Falls
// back to "{}" on a marshal error (never possible for this fixed shape, but
// LogPlatformAudit expects valid JSON regardless).
func auditDetails(ticketID, ticketNumber string) string {
	b, err := json.Marshal(map[string]string{"ticketId": ticketID, "ticketNumber": ticketNumber})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// FeedbackAdminOps handles the platform-admin side of feedback tickets: the
// cross-tenant "Support Tickets" list, status/priority/assignment changes,
// replies, and attachment downloads. Registered on /api/platform/feedback*
// behind plain RequireAuth — the admin check happens inside each handler via
// requireAdmin, matching TenantOps's requirePlatformAdmin convention.
type FeedbackAdminOps struct {
	CP *tenancy.ControlPlane
	r2 *storage.Client
}

// NewFeedbackAdminOps constructs the handler group.
func NewFeedbackAdminOps(cp *tenancy.ControlPlane, r2 *storage.Client) *FeedbackAdminOps {
	return &FeedbackAdminOps{CP: cp, r2: r2}
}

func (h *FeedbackAdminOps) requireAdmin(r *http.Request) (middleware.UserContextPayload, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil {
		return payload, false
	}
	ok, err := h.CP.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil || !ok {
		return payload, false
	}
	return payload, true
}

// ---- GET /api/platform/feedback ----------------------------------------------

// List returns tickets across every tenant, newest first, keyset paginated,
// with optional status/category/priority/tenant/search filters.
func (h *FeedbackAdminOps) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(r); !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}

	q := r.URL.Query()
	f := feedback.AdminFilter{
		Status:   q.Get("status"),
		Category: q.Get("category"),
		Priority: q.Get("priority"),
		TenantID: q.Get("tenantId"),
		Search:   q.Get("search"),
		Cursor:   q.Get("cursor"),
	}
	if f.Status != "" && !feedback.ValidStatus(f.Status) {
		fail(w, http.StatusBadRequest, "Invalid status filter.")
		return
	}
	if f.Category != "" && !feedback.ValidCategory(f.Category) {
		fail(w, http.StatusBadRequest, "Invalid category filter.")
		return
	}
	if f.Priority != "" && !feedback.ValidPriority(f.Priority) {
		fail(w, http.StatusBadRequest, "Invalid priority filter.")
		return
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}

	tickets, next, err := feedback.ListForAdmin(r.Context(), h.CP.Pool(), f)
	if errors.Is(err, feedback.ErrInvalidCursor) {
		fail(w, http.StatusBadRequest, "Invalid pagination cursor.")
		return
	}
	if err != nil {
		log.Printf("feedback.ListForAdmin: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to list feedback tickets.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tickets": tickets, "nextCursor": next})
}

// ---- GET /api/platform/feedback/stats -----------------------------------------

// Stats returns the current ticket count by status, for the list page's
// summary tiles.
func (h *FeedbackAdminOps) Stats(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(r); !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}
	stats, err := feedback.GetStats(r.Context(), h.CP.Pool())
	if err != nil {
		log.Printf("feedback.GetStats: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback stats.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "stats": stats})
}

// ---- GET /api/platform/feedback/{id} -------------------------------------------

// Get returns one ticket (any tenant) with its full timeline — including
// internal-only notes — and attachments, for the admin detail page.
func (h *FeedbackAdminOps) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(r); !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}
	id := r.PathValue("id")

	ticket, err := feedback.GetForAdmin(r.Context(), h.CP.Pool(), id)
	if errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	}
	if err != nil {
		log.Printf("feedback.GetForAdmin: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}
	comments, err := feedback.ListComments(r.Context(), h.CP.Pool(), id, true)
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

// ---- PATCH /api/platform/feedback/{id} -----------------------------------------

type patchFeedbackRequest struct {
	Status                  *string `json:"status,omitempty"`
	Priority                *string `json:"priority,omitempty"`
	AssignedAdminIdentityID *string `json:"assignedAdminIdentityId,omitempty"`
	InternalNotes           *string `json:"internalNotes,omitempty"`
}

// Patch updates status, priority, assignment, and/or internal notes on a
// ticket. A status change is recorded as a timeline entry the reporter sees;
// the other three fields are admin-only and never appear on a reporter route.
func (h *FeedbackAdminOps) Patch(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.requireAdmin(r)
	if !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}
	id := r.PathValue("id")

	existing, err := feedback.GetForAdmin(r.Context(), h.CP.Pool(), id)
	if errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	}
	if err != nil {
		log.Printf("feedback.GetForAdmin: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	var req patchFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Status == nil && req.Priority == nil && req.AssignedAdminIdentityID == nil && req.InternalNotes == nil {
		fail(w, http.StatusBadRequest, "No fields to update.")
		return
	}
	if req.Status != nil && !feedback.ValidStatus(*req.Status) {
		fail(w, http.StatusBadRequest, "Invalid status.")
		return
	}
	if req.Priority != nil && !feedback.ValidPriority(*req.Priority) {
		fail(w, http.StatusBadRequest, "Invalid priority.")
		return
	}
	if req.InternalNotes != nil {
		if err := feedback.ValidateInternalNotes(*req.InternalNotes); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.AssignedAdminIdentityID != nil && *req.AssignedAdminIdentityID != "" {
		isAdmin, err := h.CP.IsPlatformAdmin(r.Context(), *req.AssignedAdminIdentityID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to validate assignee.")
			return
		}
		if !isAdmin {
			fail(w, http.StatusBadRequest, "Assignee must be a platform admin.")
			return
		}
	}

	adminIdentity, err := h.CP.IdentityByID(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve admin identity.")
		return
	}

	if req.Status != nil {
		if err := feedback.UpdateStatus(r.Context(), h.CP.Pool(), id, *req.Status, payload.ID, feedback.AuthorPlatformAdmin, adminIdentity.FullName); err != nil {
			log.Printf("feedback.UpdateStatus: %v", err)
			fail(w, http.StatusInternalServerError, "Failed to update ticket status.")
			return
		}
	}
	if req.Priority != nil || req.AssignedAdminIdentityID != nil || req.InternalNotes != nil {
		if err := feedback.UpdateAdminFields(r.Context(), h.CP.Pool(), id, feedback.AdminPatch{
			Priority: req.Priority, AssignedAdminIdentityID: req.AssignedAdminIdentityID, InternalNotes: req.InternalNotes,
		}); err != nil {
			log.Printf("feedback.UpdateAdminFields: %v", err)
			fail(w, http.StatusInternalServerError, "Failed to update ticket.")
			return
		}
	}

	if logErr := h.CP.LogPlatformAudit(r.Context(), payload.ID, payload.Email, existing.TenantID,
		"feedback.update", auditDetails(id, existing.TicketNumber),
	); logErr != nil {
		log.Printf("platform audit log (feedback.update) ticket=%s: %v", id, logErr)
	}

	updated, err := feedback.GetForAdmin(r.Context(), h.CP.Pool(), id)
	if err != nil {
		log.Printf("feedback.GetForAdmin (post-update): %v", err)
		fail(w, http.StatusInternalServerError, "Ticket updated, but failed to reload it.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "ticket": updated})
}

// ---- POST /api/platform/feedback/{id}/comments ---------------------------------

type addAdminCommentRequest struct {
	Body       string `json:"body"`
	IsInternal bool   `json:"isInternal"`
}

// AddComment appends an admin reply (visible to the reporter) or an
// internal-only note (visible to admins only) to a ticket's timeline.
func (h *FeedbackAdminOps) AddComment(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.requireAdmin(r)
	if !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}
	id := r.PathValue("id")

	existing, err := feedback.GetForAdmin(r.Context(), h.CP.Pool(), id)
	if errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	}
	if err != nil {
		log.Printf("feedback.GetForAdmin: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}

	var req addAdminCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := feedback.ValidateComment(req.Body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	adminIdentity, err := h.CP.IdentityByID(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve admin identity.")
		return
	}

	comment, err := feedback.AddComment(r.Context(), h.CP.Pool(), feedback.AddCommentInput{
		FeedbackID: id, AuthorIdentityID: payload.ID, AuthorKind: feedback.AuthorPlatformAdmin,
		AuthorName: adminIdentity.FullName, Body: req.Body, IsInternal: req.IsInternal,
	})
	if err != nil {
		log.Printf("feedback.AddComment: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to add reply.")
		return
	}

	action := "feedback.reply"
	if req.IsInternal {
		action = "feedback.internal_note"
	}
	if logErr := h.CP.LogPlatformAudit(r.Context(), payload.ID, payload.Email, existing.TenantID,
		action, auditDetails(id, existing.TicketNumber),
	); logErr != nil {
		log.Printf("platform audit log (%s) ticket=%s: %v", action, id, logErr)
	}

	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "comment": comment})
}

// ---- GET /api/platform/feedback/{id}/attachments/{attachmentId}/download -------

// DownloadAttachment generates a short-lived presigned GET URL against the
// reporting tenant's bucket (resolved from the ticket, not the caller — the
// admin has no tenant of their own on this route).
func (h *FeedbackAdminOps) DownloadAttachment(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(r); !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	id := r.PathValue("id")
	attachmentID := r.PathValue("attachmentId")

	ticket, err := feedback.GetForAdmin(r.Context(), h.CP.Pool(), id)
	if errors.Is(err, feedback.ErrNotFound) {
		fail(w, http.StatusNotFound, "Feedback ticket not found.")
		return
	}
	if err != nil {
		log.Printf("feedback.GetForAdmin: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to load feedback ticket.")
		return
	}
	tenant, err := h.CP.TenantByID(r.Context(), ticket.TenantID)
	if err != nil || tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

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

	downloadURL, err := h.r2.WithBucket(tenant.R2Bucket).PresignGet(r.Context(), att.StorageKey, presignGetTTL)
	if err != nil {
		log.Printf("feedback admin attachment PresignGet: %v", err)
		fail(w, http.StatusInternalServerError, "Failed to generate download URL.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "downloadUrl": downloadURL, "fileName": att.FileName,
		"expiresIn": int(presignGetTTL.Seconds()),
	})
}
