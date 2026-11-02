// controllers/vendor_transactions.go
package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/dateonly"
	"stonesuite-backend/middleware"
	"stonesuite-backend/vendortransactions"
)

// vendorTxResource names this endpoint in security-event logs.
const vendorTxResource = "vendor-transactions"

// VendorTransactionsOps serves a vendor's Transactions tab: every purchasing
// document tied to the vendor across the modules the caller may read. The
// parent vendor is resolved and IDOR-guarded via VendorOps.authVendorByUUID
// (vendor:read + ownership scope, 404 on denial); each document type is then
// gated by its own grant inside the vendortransactions package.
type VendorTransactionsOps struct {
	vendor     *VendorOps
	fallbackTZ *time.Location
	// exportLimiter caps CSV exports per identity; nil disables the cap.
	exportLimiter *middleware.UserRateLimiter
}

// Export rate: one export per VendorTxExportRefill sustained, bursts of
// VendorTxExportBurst, per identity.
const (
	VendorTxExportRefill = 10 * time.Second
	VendorTxExportBurst  = 3
)

// NewVendorTransactionsOps constructs the handler group; fallback is the
// timezone used for tenants that have not set one (nil means UTC), and
// exportLimiter is the per-identity CSV export limiter (nil means unlimited).
func NewVendorTransactionsOps(fallback *time.Location, exportLimiter *middleware.UserRateLimiter) *VendorTransactionsOps {
	return &VendorTransactionsOps{vendor: NewVendorOps(), fallbackTZ: fallback, exportLimiter: exportLimiter}
}

// writeVendorTxError maps a package error to its HTTP response and reports
// whether it handled one.
func writeVendorTxError(w http.ResponseWriter, err error) bool {
	var invalid *vendortransactions.InvalidParamError
	switch {
	case err == nil:
		return false
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, "Invalid "+invalid.Param+": "+invalid.Msg+".")
	case errors.Is(err, vendortransactions.ErrVendorNotFound):
		fail(w, http.StatusNotFound, "Vendor not found.")
	default:
		slog.Error("vendor transactions: request failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load vendor transactions.")
	}
	return true
}

// parseVendorTxParams reads the list query string. It writes the 400 and
// returns false on a malformed value.
func parseVendorTxParams(w http.ResponseWriter, r *http.Request, p *vendortransactions.Params) bool {
	q := r.URL.Query()
	p.Type, p.Status, p.Search, p.Cursor = q.Get("type"), q.Get("status"), q.Get("q"), q.Get("cursor")
	for _, d := range []struct {
		name string
		dst  **dateonly.Date
	}{{"from", &p.From}, {"to", &p.To}} {
		v := q.Get(d.name)
		if v == "" {
			continue
		}
		parsed, err := dateonly.Parse(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "Invalid "+d.name+": must be a date (YYYY-MM-DD).")
			return false
		}
		*d.dst = &parsed
	}
	if v := q.Get("open"); v != "" {
		open, err := strconv.ParseBool(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "Invalid open: must be true or false.")
			return false
		}
		p.OpenOnly = open
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "Invalid limit: must be a non-negative integer.")
			return false
		}
		p.Limit = n
	}
	return true
}

// List GET /api/tenant/vendors/{uuid}/transactions
//
// Query params (all optional): type, status, from, to (yyyy-mm-dd), q, open,
// cursor, limit. The response carries nextCursor ("" on the last page).
func (h *VendorTransactionsOps) List(w http.ResponseWriter, r *http.Request) {
	vendorUUID := r.PathValue("uuid")
	pool, identityID, _, ok := h.vendor.authVendorByUUID(w, r, vendorUUID, authz.ActionRead)
	if !ok {
		return
	}
	params := vendortransactions.Params{IdentityID: identityID, VendorUUID: vendorUUID}
	if !parseVendorTxParams(w, r, &params) {
		return
	}
	params.Today = tenantToday(r.Context(), pool, h.fallbackTZ)

	page, err := vendortransactions.List(r.Context(), pool, params)
	if writeVendorTxError(w, err) {
		return
	}
	if page.Denied {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "record", vendorUUID, "resource", vendorTxResource,
			"action", string(authz.ActionRead), "type", params.Type)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"rows":       page.Rows,
		"nextCursor": page.NextCursor,
		"limit":      page.Limit,
	})
}

// Summary GET /api/tenant/vendors/{uuid}/transactions/summary
//
// The unfiltered payables figures, per-type counts and readable types.
func (h *VendorTransactionsOps) Summary(w http.ResponseWriter, r *http.Request) {
	vendorUUID := r.PathValue("uuid")
	pool, identityID, _, ok := h.vendor.authVendorByUUID(w, r, vendorUUID, authz.ActionRead)
	if !ok {
		return
	}
	ov, err := vendortransactions.GetOverview(r.Context(), pool, identityID, vendorUUID, tenantToday(r.Context(), pool, h.fallbackTZ))
	if writeVendorTxError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"scopeLimited": ov.ScopeLimited,
		"summary":      ov.Summary,
		"counts":       ov.Counts,
		"types":        ov.Types,
	})
}
