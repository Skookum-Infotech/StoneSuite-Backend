//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/portal"
	"stonesuite-backend/tenancy"
)

const (
	portalLoginHeader = "X-Test-Portal-Login"
	malformedPathID   = "not-a-uuid"
	// unknownPathID is a well-formed id no row has, so it exercises the same
	// not-found path as a record that belongs to another customer.
	unknownPathID = "00000000-0000-4000-8000-000000000000"
)

// portalRouteEnv is the /api/portal document surface mounted on a real mux, the
// way main.go registers it, with two customers each holding one visible payment.
type portalRouteEnv struct {
	handler      http.Handler
	ownLogin     string
	ownPayment   string
	otherPayment string
}

// portalDocSlugs are the four document kinds a portal customer can read.
var portalDocSlugs = []string{"sales-orders", "invoices", "payments", "refunds"}

// seedPortalCustomer inserts a live CUST customer with one active portal login
// and returns the customer id and the login's identity id.
func seedPortalCustomer(t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	ctx := context.Background()
	custID := seedCustomerRecord(t, pool)
	var identity string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer_portal_user (identity_id, customer_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, $2, 'Portal User', 'active') RETURNING identity_id`,
		custID, fmt.Sprintf("portal-%d@example.com", time.Now().UnixNano())).Scan(&identity))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM customer_portal_user WHERE customer_id = $1`, custID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM payment WHERE payment_customer_id = $1`, custID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM customer WHERE customer_id = $1`, custID)
	})
	return custID, identity
}

// seedVisiblePayment inserts an approved payment, which the portal may show.
func seedVisiblePayment(t *testing.T, pool *pgxpool.Pool, customerID int) string {
	t.Helper()
	var uuid string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO payment (payment_number, record_type, payment_status, payment_customer_id, payment_method, payment_amount, payment_created_by)
		SELECT $2, rt.record_type_id, rs.record_status_id, $1,
		       (SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'), 10.00, 1
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = 'APPV'
		WHERE rt.record_type_code = 'PYMT'
		RETURNING payment_uuid`, customerID, fmt.Sprintf("PT%d", time.Now().UnixNano()%1e15)).Scan(&uuid))
	return uuid
}

func newPortalRouteEnv(t *testing.T) *portalRouteEnv {
	t.Helper()
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	ownID, ownLogin := seedPortalCustomer(t, pool)
	otherID, _ := seedPortalCustomer(t, pool)
	env := &portalRouteEnv{
		ownLogin:     ownLogin,
		ownPayment:   seedVisiblePayment(t, pool, ownID),
		otherPayment: seedVisiblePayment(t, pool, otherID),
	}

	// chain stands in for RequireAuth + RequirePortal: it puts the calling login
	// in the context, then resolves the tenant exactly as production does.
	resolver := tenancy.NewResolver(cp, newTestRouter(t))
	chain := func(h http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := middleware.UserContextPayload{ID: r.Header.Get(portalLoginHeader), TenantID: tenant.ID}
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, payload)
			resolver.Middleware(h).ServeHTTP(w, r.WithContext(ctx))
		})
	}

	docs, orders := &PortalDocumentOps{}, NewPortalCreatedOrderOps()
	mux := http.NewServeMux()
	mux.Handle("GET /api/portal/sales-orders/{uuid}", chain(docs.GetSalesOrder))
	mux.Handle("GET /api/portal/invoices/{uuid}", chain(docs.GetInvoice))
	mux.Handle("GET /api/portal/payments/{uuid}", chain(docs.GetPayment))
	mux.Handle("GET /api/portal/refunds/{uuid}", chain(docs.GetRefund))
	for _, slug := range portalDocSlugs {
		mod, ok := portal.ModuleForURL(slug)
		require.True(t, ok, slug)
		mux.Handle("/api/portal/"+slug+"/{uuid}/messages", chain(docs.MessagesFor(mod)))
	}
	mux.Handle("GET /api/portal/fabrication-jobs/{uuid}/templates/{revision}", chain(docs.GetFabricationTemplate))
	mux.Handle("POST /api/portal/fabrication-jobs/{uuid}/templates/{revision}/approve", chain(docs.ApproveFabricationTemplate))
	mux.Handle("GET /api/portal/created-orders/{id}", chain(orders.Get))
	mux.Handle("PUT /api/portal/created-orders/{id}", chain(orders.Update))
	env.handler = mux
	return env
}

func (e *portalRouteEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(portalLoginHeader, e.ownLogin)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// Every portal route that takes an id must treat a malformed one as the caller's
// mistake: a 400, never the 500 an outage gets.
func TestPortalRoutes_MalformedID_DB(t *testing.T) {
	env := newPortalRouteEnv(t)
	const revision = unknownPathID

	type route struct {
		name, method, path, body string
		want                     int
	}
	var routes []route
	for _, slug := range portalDocSlugs {
		base := "/api/portal/" + slug + "/" + malformedPathID
		routes = append(routes,
			route{"GET " + slug + "/{uuid}", http.MethodGet, base, "", http.StatusBadRequest},
			route{"GET " + slug + "/{uuid}/messages", http.MethodGet, base + "/messages", "", http.StatusBadRequest},
			route{"POST " + slug + "/{uuid}/messages", http.MethodPost, base + "/messages", `{"body":"hello"}`, http.StatusBadRequest},
		)
	}
	fab := "/api/portal/fabrication-jobs/" + malformedPathID + "/templates/" + revision
	routes = append(routes,
		route{"GET fabrication template", http.MethodGet, fab, "", http.StatusBadRequest},
		route{"POST fabrication template approve", http.MethodPost, fab + "/approve", `{}`, http.StatusBadRequest},
		// The created-order form checks the id's shape itself and answers 404, so
		// ids stay un-probeable. That is not the outage this guards against.
		route{"GET created-orders/{id}", http.MethodGet, "/api/portal/created-orders/" + malformedPathID, "", http.StatusNotFound},
		route{"PUT created-orders/{id}", http.MethodPut, "/api/portal/created-orders/" + malformedPathID, `{}`, http.StatusNotFound},
	)

	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(tc.method, tc.path, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			assert.NotEqual(t, http.StatusInternalServerError, rec.Code)
			var resp struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
			assert.False(t, resp.Success)
			assert.NotEmpty(t, resp.Message)
		})
	}
}

// A well-formed id the caller may not see answers exactly like one that does not
// exist, so a customer can never confirm another customer's record is there. The
// caller's own record still loads.
func TestPortalPayment_Visibility_DB(t *testing.T) {
	env := newPortalRouteEnv(t)
	other := "/api/portal/payments/" + env.otherPayment
	own := "/api/portal/payments/" + env.ownPayment
	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"another customer's payment", http.MethodGet, other, "", http.StatusNotFound},
		{"an id no row has", http.MethodGet, "/api/portal/payments/" + unknownPathID, "", http.StatusNotFound},
		{"another customer's payment thread", http.MethodGet, other + "/messages", "", http.StatusNotFound},
		{"posting to another customer's payment thread", http.MethodPost, other + "/messages", `{"body":"hi"}`, http.StatusNotFound},
		{"own payment", http.MethodGet, own, "", http.StatusOK},
		{"own payment thread", http.MethodGet, own + "/messages", "", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(tc.method, tc.path, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
