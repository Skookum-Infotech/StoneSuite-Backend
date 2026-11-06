package controllers

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
	"stonesuite-backend/tenancy"
)

// reportDateLayout is the YYYY-MM-DD form the Reports pages send for a window.
const reportDateLayout = "2006-01-02"

// ReportsOps serves the Reports module's read-only aggregate endpoints.
type ReportsOps struct{}

// NewReportsOps constructs the handler group.
func NewReportsOps() *ReportsOps { return &ReportsOps{} }

// salesReportRequest is a parsed + authorized Sales report call.
type salesReportRequest struct {
	filter reports.Filter
	bucket reports.Bucket
	pool   *pgxpool.Pool
}

// parseReportDate reads an optional YYYY-MM-DD query value; absent is the
// zero time (unbounded on that side).
func parseReportDate(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(reportDateLayout, raw)
	return t, err == nil
}

// parseSalesFilter validates the shared query parameters (basis, from, to,
// bucket). It returns a human message when one is invalid.
func parseSalesFilter(r *http.Request) (reports.Filter, reports.Bucket, string) {
	q := r.URL.Query()
	basis, ok := reports.ParseBasis(q.Get("basis"))
	if !ok {
		return reports.Filter{}, "", "basis must be orders or invoices."
	}
	bucket, ok := reports.ParseBucket(q.Get("bucket"))
	if !ok {
		return reports.Filter{}, "", "bucket must be week or month."
	}
	from, ok := parseReportDate(q.Get("from"))
	if !ok {
		return reports.Filter{}, "", "from must be a YYYY-MM-DD date."
	}
	to, ok := parseReportDate(q.Get("to"))
	if !ok {
		return reports.Filter{}, "", "to must be a YYYY-MM-DD date."
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return reports.Filter{}, "", "to must not be before from."
	}
	return reports.Filter{Basis: basis, From: from, To: to}, bucket, ""
}

// authorizeSalesReport runs the checks every Sales report shares: GET only,
// signed in, report_sales:read, valid parameters. On failure it has already
// written the response and ok is false.
func (h *ReportsOps) authorizeSalesReport(w http.ResponseWriter, r *http.Request) (salesReportRequest, bool) {
	var req salesReportRequest
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return req, false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return req, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return req, false
	}
	filter, bucket, msg := parseSalesFilter(r)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return req, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReportSales, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return req, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to view sales reports.")
		return req, false
	}
	filter.Scope = decision.Scope
	filter.ActorIdentityID = payload.ID
	return salesReportRequest{filter: filter, bucket: bucket, pool: pool}, true
}

// reportResponse is the envelope of every aggregate report.
type reportResponse[T any] struct {
	Success bool           `json:"success"`
	Basis   reports.Basis  `json:"basis"`
	Totals  reports.Totals `json:"totals"`
	Rows    []T            `json:"rows"`
}

// SalesByCustomer serves Reports > Sales > Sales by Customer.
// GET /api/tenant/reports/sales/by-customer?basis=orders|invoices&from=&to=
func (h *ReportsOps) SalesByCustomer(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSalesReport(w, r)
	if !ok {
		return
	}
	rows, totals, err := reports.SalesByCustomer(r.Context(), req.pool, req.filter)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, reportResponse[reports.CustomerRow]{Success: true, Basis: req.filter.Basis, Totals: totals, Rows: rows})
}

// SalesByItem serves Reports > Sales > Sales by Item.
// GET /api/tenant/reports/sales/by-item?basis=orders|invoices&from=&to=
func (h *ReportsOps) SalesByItem(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSalesReport(w, r)
	if !ok {
		return
	}
	rows, totals, err := reports.SalesByItem(r.Context(), req.pool, req.filter)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, reportResponse[reports.ItemRow]{Success: true, Basis: req.filter.Basis, Totals: totals, Rows: rows})
}

// SalesTrend serves Reports > Sales > Sales Trend.
// GET /api/tenant/reports/sales/trend?basis=&from=&to=&bucket=week|month
func (h *ReportsOps) SalesTrend(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSalesReport(w, r)
	if !ok {
		return
	}
	rows, totals, err := reports.SalesTrend(r.Context(), req.pool, req.filter, req.bucket)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, reportResponse[reports.TrendRow]{Success: true, Basis: req.filter.Basis, Totals: totals, Rows: rows})
}

// drillResponse is the side panel's payload: the documents behind one row.
type drillResponse struct {
	Success bool                  `json:"success"`
	Basis   reports.Basis         `json:"basis"`
	Path    string                `json:"path"`
	Records []reports.DrillRecord `json:"records"`
}

// SalesRecords serves the drill-down panel: the orders/invoices behind one
// report row. Exactly one of customerId, itemId or period must be given.
// GET /api/tenant/reports/sales/records?basis=&from=&to=&customerId=|itemId=|period=YYYY-MM-DD&bucket=
func (h *ReportsOps) SalesRecords(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSalesReport(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sel := reports.DrillSelector{
		CustomerUUID: q.Get("customerId"),
		ItemUUID:     q.Get("itemId"),
		Bucket:       req.bucket,
	}
	if raw := q.Get("period"); raw != "" {
		period, valid := parseReportDate(raw)
		if !valid {
			fail(w, http.StatusBadRequest, "period must be a YYYY-MM-DD date.")
			return
		}
		sel.PeriodStart = period
	}
	if sel.CustomerUUID == "" && sel.ItemUUID == "" && sel.PeriodStart.IsZero() {
		fail(w, http.StatusBadRequest, "Give a customerId, itemId or period.")
		return
	}
	// A malformed id must be a 400, not a Postgres cast error surfaced as 500.
	for _, id := range []string{sel.CustomerUUID, sel.ItemUUID} {
		if id != "" && !reports.IsUUID(id) {
			fail(w, http.StatusBadRequest, "customerId and itemId must be valid ids.")
			return
		}
	}
	records, err := reports.DrillRecords(r.Context(), req.pool, req.filter, sel)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not load the records.")
		return
	}
	writeJSON(w, http.StatusOK, drillResponse{Success: true, Basis: req.filter.Basis, Path: reports.RecordPath(req.filter.Basis), Records: records})
}
