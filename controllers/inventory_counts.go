package controllers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventorycount"
	"stonesuite-backend/query"
)

// InventoryCountOps serves cycle counting and physical stock takes.
//
// Freezing is what makes a count mean anything: it snapshots the system
// quantity onto every line and blocks movement in the counted scope until the
// count posts or is cancelled.
//
// Routes:
//
//	GET    /api/tenant/inventory/counts                   — list
//	POST   /api/tenant/inventory/counts/search            — filter + sort
//	POST   /api/tenant/inventory/counts                   — create (draft)
//	GET    /api/tenant/inventory/counts/{uuid}            — get with lines
//	PATCH  /api/tenant/inventory/counts/{uuid}            — edit a draft's scope
//	DELETE /api/tenant/inventory/counts/{uuid}            — soft delete
//	POST   /api/tenant/inventory/counts/{uuid}/freeze     — snapshot and start
//	POST   /api/tenant/inventory/counts/{uuid}/counts     — record what was found
//	POST   /api/tenant/inventory/counts/{uuid}/unexpected — log an unlisted unit
//	POST   /api/tenant/inventory/counts/{uuid}/transition — move status
//	POST   /api/tenant/inventory/counts/{uuid}/post       — apply variances
//	GET    /api/tenant/inventory/counts/{uuid}/history    — status trail
type InventoryCountOps struct{}

// NewInventoryCountOps constructs the handler group.
func NewInventoryCountOps() *InventoryCountOps { return &InventoryCountOps{} }

// List GET /api/tenant/inventory/counts
func (h *InventoryCountOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, req)
}

// Search POST /api/tenant/inventory/counts/search
func (h *InventoryCountOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	h.search(w, r, pool, req)
}

func (h *InventoryCountOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request) {
	page, err := inventorycount.Search(r.Context(), pool, req)
	if err != nil {
		countFail(w, err, "Failed to search counts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Get GET /api/tenant/inventory/counts/{uuid}
func (h *InventoryCountOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	c, err := inventorycount.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		countFail(w, err, "Failed to load count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "count": c,
		"nextStatuses": inventorycount.Machine().Next(c.StatusCode),
	})
}

// Create POST /api/tenant/inventory/counts
func (h *InventoryCountOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventorycount.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	c, err := inventorycount.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		countFail(w, err, "Failed to create count.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "count": c})
}

// Update PATCH /api/tenant/inventory/counts/{uuid}
func (h *InventoryCountOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventorycount.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventorycount.Update(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		countFail(w, err, "Failed to update count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Count updated."})
}

// Delete DELETE /api/tenant/inventory/counts/{uuid}
func (h *InventoryCountOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventorycount.Delete(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		countFail(w, err, "Failed to delete count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Count deleted."})
}

// Freeze POST /api/tenant/inventory/counts/{uuid}/freeze — snapshot the scope
// and start counting.
func (h *InventoryCountOps) Freeze(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	c, err := inventorycount.Freeze(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID))
	if err != nil {
		countFail(w, err, "Failed to freeze the count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": c})
}

// RecordCounts POST /api/tenant/inventory/counts/{uuid}/counts
func (h *InventoryCountOps) RecordCounts(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body struct {
		Entries []inventorycount.CountEntry `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	c, err := inventorycount.RecordCounts(r.Context(), pool, r.PathValue("uuid"), body.Entries, resolveEmployeeID(r, identityID))
	if err != nil {
		countFail(w, err, "Failed to record the counts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": c})
}

// AddUnexpected POST /api/tenant/inventory/counts/{uuid}/unexpected — a unit
// found in the counted scope that the frozen snapshot did not contain.
func (h *InventoryCountOps) AddUnexpected(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventorycount.UnexpectedEntry
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	c, err := inventorycount.AddUnexpected(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID))
	if err != nil {
		countFail(w, err, "Failed to add the unit to the count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": c})
}

// Transition POST /api/tenant/inventory/counts/{uuid}/transition
//
// Moving into the approved status additionally requires
// inventory_count:approve — accepting a count's variances writes off real
// stock, so it stays separate from plain transition rights.
func (h *InventoryCountOps) Transition(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if body.Status == inventorycount.StatusApproved && !h.mayApprove(w, r, pool, identityID) {
		return
	}
	c, err := inventorycount.Transition(r.Context(), pool, r.PathValue("uuid"),
		body.Status, body.Note, resolveEmployeeID(r, identityID))
	if err != nil {
		countFail(w, err, "Failed to change the count's status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": c})
}

// Post POST /api/tenant/inventory/counts/{uuid}/post — apply the variances.
func (h *InventoryCountOps) Post(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	c, err := inventorycount.Post(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID))
	if err != nil {
		countFail(w, err, "Failed to post the count.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": c})
}

// History GET /api/tenant/inventory/counts/{uuid}/history
func (h *InventoryCountOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := inventorycount.History(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		countFail(w, err, "Failed to load the count's history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": entries})
}
