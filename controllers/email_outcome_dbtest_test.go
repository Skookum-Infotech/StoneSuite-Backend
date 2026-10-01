//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/middleware"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
	"stonesuite-backend/workflow"
)

// stonesuite-notify answers a create call before it sends anything; the email
// goes out (or is refused by the provider) a moment later. These tests drive
// the real handlers against a fake notify that scripts that second half, to
// prove a provider refusal reaches the response as emailSent=false instead of
// being reported as a sent email.

// providerRefusal is the kind of text notify records for a refused send. It
// must reach the server log only, never the response body.
const providerRefusal = "resend: unexpected status 403: The unverified-sender.test domain is not verified."

// notifyScenario scripts the fake notify service.
type notifyScenario struct {
	// createStatus is the status of POST /api/notifications/internal (0 = 201).
	createStatus int
	// emailStatus / emailLastError are the email row in the delivery log.
	emailStatus    string
	emailLastError string
	// statusState is the state POST /api/deliveries/email-status reports for
	// every id asked about ("" = answer with an empty map). statusHTTP makes
	// that route fail with the given HTTP status (0 = 200).
	statusState string
	statusHTTP  int
	// onCreate, when set, is told the actorUserId of each create call.
	onCreate func(actorUserID string)
}

// withFakeNotify points the app at a fake stonesuite-notify for one test.
func withFakeNotify(t *testing.T, sc notifyScenario) {
	t.Helper()
	var ids atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/notifications/internal":
			if sc.createStatus != 0 && sc.createStatus != http.StatusCreated {
				w.WriteHeader(sc.createStatus)
				return
			}
			if sc.onCreate != nil {
				var req struct {
					ActorUserID string `json:"actorUserId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				sc.onCreate(req.ActorUserID)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"notifications":[{"id":"n-%d"}]}}`, ids.Add(1))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deliveries"):
			body, _ := json.Marshal(map[string]any{"success": true, "data": map[string]any{"deliveries": []map[string]any{
				{"channel": "in_app", "status": "skipped"},
				{"channel": "email", "status": sc.emailStatus, "lastError": sc.emailLastError},
			}}})
			_, _ = w.Write(body)
		case r.Method == http.MethodPost && r.URL.Path == "/api/deliveries/email-status":
			if sc.statusHTTP != 0 {
				w.WriteHeader(sc.statusHTTP)
				return
			}
			var req struct {
				NotificationIDs []string `json:"notificationIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			statuses := map[string]any{}
			if sc.statusState != "" {
				for _, id := range req.NotificationIDs {
					statuses[id] = map[string]any{"state": sc.statusState, "recipient": "bob@buyer.example",
						"detail": providerRefusal, "updatedAt": "2026-10-01T12:00:00Z"}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"statuses": statuses}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	orig := config.AppConfig
	config.AppConfig.NotifyURL = srv.URL
	config.AppConfig.NotifyAPIKey = "nk_dbtest_outcome_secret"
	t.Cleanup(func() { config.AppConfig = orig })
}

// outcomeScenarios are the three ways an email can end, from the caller's view.
var outcomeScenarios = []struct {
	name        string
	sc          notifyScenario
	wantSent    bool
	wantErrText string
}{
	{
		name:     "provider accepted the email",
		sc:       notifyScenario{emailStatus: "sent"},
		wantSent: true,
	},
	{
		name:        "provider refused the first attempt (notify already said created)",
		sc:          notifyScenario{emailStatus: "retrying", emailLastError: providerRefusal},
		wantSent:    false,
		wantErrText: services.EmailOutcome{Status: services.EmailStatusFailed}.UserMessage(),
	},
	{
		name:        "notify itself refused the request",
		sc:          notifyScenario{createStatus: http.StatusInternalServerError},
		wantSent:    false,
		wantErrText: services.EmailOutcome{Status: services.EmailStatusFailed}.UserMessage(),
	},
}

// assertEmailOutcome checks the response body carries the honest email fields
// and never leaks the provider's text.
func assertEmailOutcome(t *testing.T, body string, wantSent bool, wantErrText string) {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &got), "body: %s", body)

	assert.Equal(t, wantSent, got["emailSent"], "body: %s", body)
	if wantSent {
		assert.NotContains(t, got, "emailError")
	} else {
		assert.Equal(t, wantErrText, got["emailError"])
	}
	assert.NotContains(t, body, "not verified", "provider detail must stay in the server log")
	// Not a bare "403": a random invite token or id can contain those digits.
	assert.NotContains(t, body, "status 403")
}

// TestUserInvite_EmailOutcome_DB drives POST /api/tenant/users/invite: the
// invite is created either way, and emailSent says whether the email went.
func TestUserInvite_EmailOutcome_DB(t *testing.T) {
	env := newInviteTestEnv(t)

	for _, tt := range outcomeScenarios {
		t.Run(tt.name, func(t *testing.T) {
			withFakeNotify(t, tt.sc)
			email := inviteTestEmail("outcome")

			rec, _ := env.invite(t, map[string]any{"email": email, "fullName": "Outcome Invitee"})

			require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
			assertEmailOutcome(t, rec.Body.String(), tt.wantSent, tt.wantErrText)
			invites := env.invitesFor(t, email)
			require.Len(t, invites, 1, "the invite is created even when the email is not")
			assert.Equal(t, "pending", invites[0].Status)
		})
	}
}

// docSendOutcomeEnv is a servable tenant with one sales order and a sender
// allowed to update it, plus the real Send handler behind the tenancy resolver.
type docSendOutcomeEnv struct {
	pool         *pgxpool.Pool
	handler      http.Handler
	sendsHandler http.Handler
	orderID      string
	request      func() *http.Request
	sendsRequest func() *http.Request
}

func newDocSendOutcomeEnv(t *testing.T) *docSendOutcomeEnv {
	t.Helper()
	cp := docSendTestControlPlane(t)
	tenantDSN := docSendTestTenantDSN(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	tenant, err := cp.CreateTenant(ctx, "docsend-outcome-"+suffix, "Doc Send Outcome Tenant", false)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "docsend-outcome-db", tenantDSN, 1))
	tenant, err = cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)

	router := tenancy.NewRouter(nil)
	t.Cleanup(router.Close)
	pool, err := router.PoolFor(ctx, tenant)
	require.NoError(t, err)

	custUUID, itemUUID := seedDocSendCustomerAndItem(t, pool)
	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.Items = []salesorder.LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	order, err := salesorder.Create(ctx, pool, in, 1)
	require.NoError(t, err)

	identityID, err := newAttachUUID()
	require.NoError(t, err)
	usr, err := userstore.CreateUser(ctx, pool, identityID, "sender-"+suffix+"@example.com", "Sender", "active")
	require.NoError(t, err)
	require.NoError(t, authz.SeedTenantRBAC(ctx, pool, usr.ID))

	docOps := NewDocumentOps(map[string]DocumentLoader{
		"sales_order": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
			so, err := salesorder.Get(ctx, pool, uuid)
			if err != nil {
				return docpdf.PrintableDoc{}, DocMeta{}, fmt.Errorf("load sales order: %w", err)
			}
			return salesorder.ToPrintable(*so, seller), DocMeta{
				WorkflowKey: "sales_order", Number: so.Number, DefaultSubject: "Your Sales Order " + so.Number,
			}, nil
		},
	}, nil, nil)
	docOps.renderPDF = func(docpdf.PrintableDoc) ([]byte, error) { return []byte("%PDF-1.4 x"), nil }

	return &docSendOutcomeEnv{
		pool:         pool,
		handler:      tenancy.NewResolver(cp, router).Middleware(http.HandlerFunc(docOps.Send)),
		sendsHandler: tenancy.NewResolver(cp, router).Middleware(http.HandlerFunc(docOps.Sends)),
		orderID:      order.ID,
		sendsRequest: func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/records/"+order.ID+"/document/sends", nil)
			req.SetPathValue("id", order.ID)
			return req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
				middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}))
		},
		request: func() *http.Request {
			req := httptest.NewRequest(http.MethodPost, "/api/tenant/records/"+order.ID+"/document/send",
				strings.NewReader(`{"to":["bob@buyer.example"]}`))
			req.SetPathValue("id", order.ID)
			return req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
				middleware.UserContextPayload{ID: identityID, TenantID: tenant.ID}))
		},
	}
}

// TestDocumentSend_EmailOutcome_DB drives POST .../document/send. The send is
// recorded when notify accepted it, and emailSent says whether the email went;
// only a request notify refused outright is an HTTP error.
func TestDocumentSend_EmailOutcome_DB(t *testing.T) {
	for _, tt := range outcomeScenarios {
		t.Run(tt.name, func(t *testing.T) {
			env := newDocSendOutcomeEnv(t)
			withFakeNotify(t, tt.sc)
			rr := httptest.NewRecorder()

			env.handler.ServeHTTP(rr, env.request())

			sends, err := workflow.ListDocumentSends(context.Background(), env.pool, env.orderID)
			require.NoError(t, err)
			if tt.sc.createStatus != 0 {
				// Notify refused the request: a plain send failure, nothing recorded.
				assert.Equal(t, http.StatusBadGateway, rr.Code, "body: %s", rr.Body.String())
				assert.Empty(t, sends)
				return
			}
			require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
			assertEmailOutcome(t, rr.Body.String(), tt.wantSent, tt.wantErrText)
			require.Len(t, sends, 1, "the send is recorded whether or not the provider accepted it")
		})
	}
}

// TestDocumentSends_ReportsRealEmailStatus_DB: after a send, the history shows
// what notify reports — and degrades to "unknown", never an error, when the
// status route is down.
func TestDocumentSends_ReportsRealEmailStatus_DB(t *testing.T) {
	tests := []struct {
		name       string
		sc         notifyScenario
		wantStatus string
		wantMsg    bool
	}{
		{"bounced", notifyScenario{emailStatus: "sent", statusState: "bounced"}, "bounced", true},
		{"delivered", notifyScenario{emailStatus: "sent", statusState: "delivered"}, "delivered", false},
		{"notify status route down", notifyScenario{emailStatus: "sent", statusHTTP: http.StatusInternalServerError}, "unknown", false},
		{"notify knows nothing", notifyScenario{emailStatus: "sent"}, "unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newDocSendOutcomeEnv(t)
			withFakeNotify(t, tt.sc)
			env.handler.ServeHTTP(httptest.NewRecorder(), env.request()) // creates one send

			rr := httptest.NewRecorder()
			env.sendsHandler.ServeHTTP(rr, env.sendsRequest())

			require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
			var resp struct {
				Sends []map[string]any `json:"sends"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
			require.Len(t, resp.Sends, 1)
			assert.Equal(t, tt.wantStatus, resp.Sends[0]["emailStatus"])
			if tt.wantMsg {
				assert.NotEmpty(t, resp.Sends[0]["emailStatusMessage"])
			} else {
				assert.NotContains(t, resp.Sends[0], "emailStatusMessage")
			}
			assert.NotContains(t, rr.Body.String(), "not verified", "provider detail must stay server-side")
			assert.NotContains(t, rr.Body.String(), "403")
		})
	}
}
