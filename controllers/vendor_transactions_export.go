package controllers

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/vendortransactions"
	"stonesuite-backend/workflow"
)

const (
	// vendorTxExportAction is the audit action for a CSV export.
	vendorTxExportAction = "export"
	// vendorTxExportContentType is the CSV response media type.
	vendorTxExportContentType = "text/csv; charset=utf-8"
	// vendorTxExportMaxMsg is the 413 message.
	vendorTxExportMaxMsg = "Too many transactions to export. Narrow the filters and try again."
)

// Export GET /api/tenant/vendors/{uuid}/transactions/export.csv
//
// Streams the vendor's transactions (same filters and scope as List; cursor and
// limit are ignored) as CSV. 413 over vendortransactions.ExportMaxRows, 429 when
// the caller's per-identity export budget is spent.
func (h *VendorTransactionsOps) Export(w http.ResponseWriter, r *http.Request) {
	vendorUUID := r.PathValue("uuid")
	pool, identityID, _, ok := h.vendor.authVendorByUUID(w, r, vendorUUID, authz.ActionRead)
	if !ok {
		return
	}
	if h.exportLimiter != nil && !h.exportLimiter.Allow(identityID) {
		logSecurityEvent(r, "rate_limited", "identity", identityID, "record", vendorUUID, "resource", vendorTxResource)
		w.Header().Set("Retry-After", strconv.Itoa(h.exportLimiter.RetryAfterSeconds()))
		failWithCode(w, http.StatusTooManyRequests, "Too many exports. Please wait a moment and try again.", models.CodeRateLimited)
		return
	}
	params := vendortransactions.Params{IdentityID: identityID, VendorUUID: vendorUUID}
	if !parseVendorTxParams(w, r, &params) {
		return
	}
	params.Cursor, params.Limit = "", 0
	params.Today = tenantToday(r.Context(), pool, h.fallbackTZ)

	rows, denied, err := vendortransactions.ExportRows(r.Context(), pool, params)
	if errors.Is(err, vendortransactions.ErrExportTooLarge) {
		fail(w, http.StatusRequestEntityTooLarge, vendorTxExportMaxMsg)
		return
	}
	if writeVendorTxError(w, err) {
		return
	}
	if denied {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "record", vendorUUID, "resource", vendorTxResource,
			"action", string(authz.ActionRead), "type", params.Type)
	}
	var buf bytes.Buffer
	if err := vendortransactions.WriteCSV(&buf, rows); err != nil {
		slog.Error("vendor transactions: csv export failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to export vendor transactions.")
		return
	}
	auditVendorTxExport(r, pool, identityID, vendorUUID, params, len(rows))

	w.Header().Set("Content-Type", vendorTxExportContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="vendor-`+vendorUUID+`-transactions.csv"`)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		slog.Warn("vendor transactions: csv write failed", "error", err)
	}
}

// auditVendorTxExport records who exported which vendor's transactions with
// which filters and how many rows -- never row contents. Best-effort.
func auditVendorTxExport(r *http.Request, pool workflow.Querier, identityID, vendorUUID string, p vendortransactions.Params, count int) {
	ctx := r.Context()
	filters := map[string]any{"type": p.Type, "status": p.Status, "q": p.Search, "open": p.OpenOnly}
	if p.From != nil {
		filters["from"] = p.From.String()
	}
	if p.To != nil {
		filters["to"] = p.To.String()
	}
	meta := map[string]any{"filters": filters, "rowCount": count}
	actorUserID := ""
	if payload, err := middleware.GetUserFromContext(ctx); err == nil {
		actorUserID = payload.UserID
	}
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, vendorTxExportAction, "vendor_transactions", vendorUUID, "vendor",
		nil, nil, meta, clientIP(r), r.Header.Get(sessionIDHeader), appVersion); err != nil {
		slog.WarnContext(ctx, "vendor transactions: export audit failed", "vendor", vendorUUID, "error", err)
	}
}
