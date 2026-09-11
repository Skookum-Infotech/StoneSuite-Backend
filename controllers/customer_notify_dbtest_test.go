//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

const notifyTestStaffIdentityID = "11111111-1111-1111-1111-111111111111"

// seedNotifyCustomer inserts a minimal live customer row -- the customer
// half of document_send_dbtest_test.go's seedDocSendCustomerAndItem, without
// the inventory item this test doesn't need.
func seedNotifyCustomer(t *testing.T, pool *pgxpool.Pool) (custUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID))

	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Customer Notify Test "+suffix).Scan(&custUUID))
	return custUUID
}

// TestNotifyCustomerApproved_DB asserts notifyCustomerApproved sends a
// notification when the customer has an active portal login, and silently
// no-ops (no error, no notify call) when they have none or it was revoked.
func TestNotifyCustomerApproved_DB(t *testing.T) {
	cp := docSendTestControlPlane(t)
	tenantDSN := docSendTestTenantDSN(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	tenant, err := cp.CreateTenant(ctx, "custnotify-test-"+suffix, "Customer Notify Test Tenant", false)
	require.NoError(t, err)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "custnotify-test-db", tenantDSN, 1))
	tenant, err = cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)

	router := tenancy.NewRouter(nil)
	t.Cleanup(router.Close)
	pool, err := router.PoolFor(ctx, tenant)
	require.NoError(t, err)

	type capturedNotify struct {
		EventType  string `json:"eventType"`
		Resource   string `json:"resource"`
		Link       string `json:"link"`
		Recipients []struct {
			UserID string `json:"userId"`
			Email  string `json:"email"`
		} `json:"recipients"`
	}
	var (
		notifyMu       sync.Mutex
		notifyRequests []capturedNotify
	)
	notifyStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body capturedNotify
		_ = json.NewDecoder(r.Body).Decode(&body)
		notifyMu.Lock()
		notifyRequests = append(notifyRequests, body)
		notifyMu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"notif-1"}]}}`))
	}))
	t.Cleanup(notifyStub.Close)
	config.AppConfig = config.Config{NotifyURL: notifyStub.URL, NotifyAPIKey: "nk_dbtest_stub_secret"}

	resolver := tenancy.NewResolver(cp, router)

	// callNotify drives notifyCustomerApproved through the real tenant-
	// resolution middleware, resetting the captured-requests slice first so
	// each subtest only sees its own call.
	callNotify := func(t *testing.T, customerUUID, recordUUID string) {
		t.Helper()
		notifyMu.Lock()
		notifyRequests = nil
		notifyMu.Unlock()

		handler := resolver.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			notifyCustomerApproved(r.Context(), cp, notifyTestStaffIdentityID, customerUUID, "invoice", "Invoice", "INV-000123", recordUUID)
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: notifyTestStaffIdentityID, TenantID: tenant.ID}))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	}

	t.Run("customer with an active portal login gets notified", func(t *testing.T) {
		custUUID := seedNotifyCustomer(t, pool)
		customerEmail := "customer-" + suffix + "@example.com"
		_, err := cp.CreateIdentity(ctx, tenant.ID, customerEmail, "hash", "Test Customer", true)
		require.NoError(t, err)
		identity, err := cp.IdentityByEmail(ctx, customerEmail)
		require.NoError(t, err)
		invite, err := cp.CreatePortalInvite(ctx, tenant.ID, identity.ID, customerEmail,
			"Test Customer", custUUID, "tok-a-"+suffix, "", time.Now().Add(48*time.Hour))
		require.NoError(t, err)
		_, err = cp.CreatePortalLink(ctx, identity.ID, tenant.ID)
		require.NoError(t, err)
		require.NoError(t, cp.MarkPortalInviteAccepted(ctx, invite.ID))

		recordUUID := "record-a-" + suffix
		callNotify(t, custUUID, recordUUID)

		notifyMu.Lock()
		defer notifyMu.Unlock()
		require.Len(t, notifyRequests, 1)
		assert.Equal(t, "invoice.customer_approved", notifyRequests[0].EventType)
		assert.Equal(t, "/sales/invoice/"+recordUUID, notifyRequests[0].Link)
		require.Len(t, notifyRequests[0].Recipients, 1)
		assert.Equal(t, identity.ID, notifyRequests[0].Recipients[0].UserID)
		assert.Equal(t, customerEmail, notifyRequests[0].Recipients[0].Email)
	})

	t.Run("customer with no portal invite gets nothing, no error", func(t *testing.T) {
		custUUID := seedNotifyCustomer(t, pool)

		callNotify(t, custUUID, "record-b-"+suffix)

		notifyMu.Lock()
		defer notifyMu.Unlock()
		assert.Empty(t, notifyRequests, "no portal invite means nothing to notify")
	})

	t.Run("customer whose portal access was revoked gets nothing", func(t *testing.T) {
		custUUID := seedNotifyCustomer(t, pool)
		revokedEmail := "revoked-" + suffix + "@example.com"
		_, err := cp.CreateIdentity(ctx, tenant.ID, revokedEmail, "hash", "Revoked Customer", true)
		require.NoError(t, err)
		identity, err := cp.IdentityByEmail(ctx, revokedEmail)
		require.NoError(t, err)
		invite, err := cp.CreatePortalInvite(ctx, tenant.ID, identity.ID, revokedEmail,
			"Revoked Customer", custUUID, "tok-c-"+suffix, "", time.Now().Add(48*time.Hour))
		require.NoError(t, err)
		_, err = cp.CreatePortalLink(ctx, identity.ID, tenant.ID)
		require.NoError(t, err)
		require.NoError(t, cp.MarkPortalInviteAccepted(ctx, invite.ID))
		require.NoError(t, cp.RevokePortalLink(ctx, identity.ID, tenant.ID))

		callNotify(t, custUUID, "record-c-"+suffix)

		notifyMu.Lock()
		defer notifyMu.Unlock()
		assert.Empty(t, notifyRequests, "revoked portal access means nothing to notify")
	})
}
