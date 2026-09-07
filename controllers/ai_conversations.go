package controllers

import (
	"log/slog"
	"net/http"

	"stonesuite-backend/ai"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// ConversationOps serves the AI assistant's conversation history:
// /api/tenant/ai/conversations/*. A conversation is personal chat history —
// every operation on one enforces strict owner-only access (404, never 403,
// on any mismatch or missing id — the same IDOR-safe convention as
// recordInScope elsewhere in this codebase), never an RBAC scope check.
type ConversationOps struct{}

// NewConversationOps constructs the handler group. Stateless: every method
// resolves its own tenant pool from request context.
func NewConversationOps() *ConversationOps { return &ConversationOps{} }

// Create handles POST /api/tenant/ai/conversations: starts a new, empty,
// untitled conversation owned by the caller. AIOps.Ask sets its title from
// the first question asked into it.
func (h *ConversationOps) Create(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	conv, err := ai.NewConversationStore(pool).Create(r.Context(), callerUserID)
	if err != nil {
		slog.Error("ai conversation create failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to start a new conversation.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "data": conv})
}

// List handles GET /api/tenant/ai/conversations: the caller's own
// conversations, most recently active first. Never another user's, even
// under an "all" CRM scope grant — conversations aren't CRM data.
func (h *ConversationOps) List(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	convs, err := ai.NewConversationStore(pool).ListByOwner(r.Context(), callerUserID)
	if err != nil {
		slog.Error("ai conversation list failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to list conversations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": convs})
}

// Get handles GET /api/tenant/ai/conversations/{id}: one conversation plus
// its full message transcript. 404 if it doesn't exist or belongs to another
// user — never 403, so a guessed id cannot be distinguished from a real one
// that isn't the caller's.
func (h *ConversationOps) Get(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	store := ai.NewConversationStore(pool)
	conv, found, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		slog.Error("ai conversation get failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load conversation.")
		return
	}
	if !found || conv.OwnerUserID != callerUserID {
		logSecurityEvent(r, "idor_denied", "identity", payload.ID, "conversation", r.PathValue("id"))
		fail(w, http.StatusNotFound, "Conversation not found.")
		return
	}

	messages, err := store.Messages(r.Context(), conv.ID)
	if err != nil {
		slog.Error("ai conversation messages load failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load conversation messages.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"conversation": conv,
		"messages":     messages,
	}})
}

// Delete handles DELETE /api/tenant/ai/conversations/{id}. 404 on any owner
// mismatch or missing id, matching Get.
func (h *ConversationOps) Delete(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	store := ai.NewConversationStore(pool)
	conv, found, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		slog.Error("ai conversation get failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load conversation.")
		return
	}
	if !found || conv.OwnerUserID != callerUserID {
		logSecurityEvent(r, "idor_denied", "identity", payload.ID, "conversation", r.PathValue("id"))
		fail(w, http.StatusNotFound, "Conversation not found.")
		return
	}

	if err := store.Delete(r.Context(), conv.ID); err != nil {
		slog.Error("ai conversation delete failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to delete conversation.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
