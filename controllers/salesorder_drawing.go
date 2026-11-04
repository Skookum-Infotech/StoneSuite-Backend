package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/drawing"
)

// The countertop drawing editor's saved document behind a Sales Order
// (build spec §5 Task 5 / §6 backend contract). Follows the Sales Order
// itself for RBAC and scope, exactly like CustomerOrderForm does: anyone who
// can open the order can read its drawing, and sales_order:update is needed
// to save/restore one.

// salesOrderInternalID resolves the sales order's internal integer id from
// its uuid. drawing.* functions key off the integer id (matching
// sales_order_drawing's own FK to sales_order), while authSOByUUID and the
// public API work in uuids throughout.
func salesOrderInternalID(ctx context.Context, pool *pgxpool.Pool, uuid string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx, `SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1`, uuid).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve sales order internal id: %w", err)
	}
	return id, nil
}

// drawingFail maps a drawing package error to an HTTP response.
func drawingFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, drawing.ErrNotFound):
		fail(w, http.StatusNotFound, "No drawing saved yet.")
	case errors.Is(err, drawing.ErrVersionConflict):
		fail(w, http.StatusConflict, "This drawing was changed by someone else — reload and try again")
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// actorFor resolves the acting employee id (nil when unresolved) and a
// display name for the request's identity, for drawing.Save/Restore's audit
// fields -- the same two lookups UpdateCustomerOrderForm already uses.
func actorFor(r *http.Request, pool *pgxpool.Pool, identityID string) (*int, string) {
	id := resolveEmployeeID(r, identityID)
	name := staffEditorLabel(r, pool, identityID)
	if id == 0 {
		return nil, name
	}
	return &id, name
}

func drawingVersionPathValue(w http.ResponseWriter, r *http.Request) (int, bool) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		fail(w, http.StatusNotFound, "Drawing version not found.")
		return 0, false
	}
	return version, true
}

func nextCursorOrNil(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

// GetDrawing GET /api/tenant/sales-orders/{uuid}/drawing
func (h *SalesOrderOps) GetDrawing(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	soID, err := salesOrderInternalID(r.Context(), pool, uuid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load drawing.")
		return
	}
	d, err := drawing.Get(r.Context(), pool, soID)
	if err != nil {
		drawingFail(w, err, "Failed to load drawing.")
		return
	}
	d.SalesOrderID = uuid
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "drawing": d})
}

type putDrawingRequest struct {
	Areas               []drawing.Area `json:"areas"`
	DisplayUnit         string         `json:"displayUnit"`
	FractionDenominator int            `json:"fractionDenominator"`
	RecordVersion       int            `json:"recordVersion"`
}

// PutDrawing PUT /api/tenant/sales-orders/{uuid}/drawing
func (h *SalesOrderOps) PutDrawing(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in putDrawingRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	candidate := drawing.Drawing{
		SchemaVersion: drawing.CurrentSchemaVersion, Areas: in.Areas,
		DisplayUnit: in.DisplayUnit, FractionDenominator: in.FractionDenominator,
	}
	if err := drawing.Validate(candidate); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	soID, err := salesOrderInternalID(r.Context(), pool, uuid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save drawing.")
		return
	}
	actorID, actorName := actorFor(r, pool, identityID)
	saved, err := drawing.Save(r.Context(), pool, soID, candidate, &in.RecordVersion, actorID, actorName)
	if err != nil {
		drawingFail(w, err, "Failed to save drawing.")
		return
	}
	saved.SalesOrderID = uuid
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "drawing": saved})
}

// ListDrawingVersions GET /api/tenant/sales-orders/{uuid}/drawing/versions
func (h *SalesOrderOps) ListDrawingVersions(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	soID, err := salesOrderInternalID(r.Context(), pool, uuid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load drawing versions.")
		return
	}
	limit := defaultVersionPageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	items, nextCursor, err := drawing.ListVersions(r.Context(), pool, soID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		drawingFail(w, err, "Failed to load drawing versions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "items": items, "nextCursor": nextCursorOrNil(nextCursor)})
}

const defaultVersionPageSize = 20

// GetDrawingVersion GET /api/tenant/sales-orders/{uuid}/drawing/versions/{version}
func (h *SalesOrderOps) GetDrawingVersion(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	version, ok := drawingVersionPathValue(w, r)
	if !ok {
		return
	}
	soID, err := salesOrderInternalID(r.Context(), pool, uuid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load drawing version.")
		return
	}
	d, err := drawing.GetVersion(r.Context(), pool, soID, version)
	if err != nil {
		drawingFail(w, err, "Failed to load drawing version.")
		return
	}
	d.SalesOrderID = uuid
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "drawing": d})
}

type restoreDrawingVersionRequest struct {
	RecordVersion int `json:"recordVersion"`
}

// RestoreDrawingVersion POST /api/tenant/sales-orders/{uuid}/drawing/versions/{version}/restore
func (h *SalesOrderOps) RestoreDrawingVersion(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	version, ok := drawingVersionPathValue(w, r)
	if !ok {
		return
	}
	var in restoreDrawingVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	soID, err := salesOrderInternalID(r.Context(), pool, uuid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to restore drawing version.")
		return
	}
	actorID, actorName := actorFor(r, pool, identityID)
	restored, err := drawing.Restore(r.Context(), pool, soID, version, &in.RecordVersion, actorID, actorName)
	if err != nil {
		drawingFail(w, err, "Failed to restore drawing version.")
		return
	}
	restored.SalesOrderID = uuid
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "drawing": restored})
}
