# Customer Portal Approval Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When Sales Order, Invoice, Payment, or Refund approval finalizes, notify the
customer — via their existing portal bell and email, if they have an active portal login —
that it's approved.

**Architecture:** A new `tenancy.ControlPlane` method resolves a tenant-DB `customer_uuid` to
its accepted control-plane portal invite (`portal_invites` already stores both on one row). A
new controller-layer helper (`notifyCustomerApproved`) uses that plus the existing
`services.SendNotification` to fire a best-effort, non-blocking notification. Four controllers
(`InvoiceOps`, `SalesOrderOps`, `PaymentOps`, `RefundOps`) each gain a `cp *tenancy.ControlPlane`
field and call the helper right after their existing `Approve` call succeeds, only when the
just-returned record's status shows the approval actually finalized this call (not a partial
sign-off). The frontend removes the one guard hiding `NotificationBell` from portal sessions —
no new frontend routes, since portal and staff already share the same detail-page routes.

**Tech Stack:** Go 1.25 (`pgx/v5`), `stonesuite-notify` (unchanged, no work there), React 19 +
TypeScript + Vite + TanStack Query + Vitest/Testing Library (StoneSuite-WebUI).

## Global Constraints

- Errors are always checked and wrapped (`fmt.Errorf("context: %w", err)`) — never swallowed,
  except inside the notify helper itself, which is explicitly best-effort (matches every other
  `Notify*` function's failure contract in this codebase — see
  `approvalchain/notify.go`).
- No global mutable state — `cp` is injected via constructor, not a package-level var.
- Structured logging only (`log/slog`), never `log.Printf`, in request-path packages.
- Tests are table-driven where the logic is pure; `dbtest`-tagged (`//go:build dbtest`) where a
  real database is required, and must skip cleanly without `TEST_DATABASE_URL`.
- Never run raw SQL in handlers — this plan's SQL lives in `tenancy` (the control-plane store
  layer), matching where every other control-plane query already lives.
- Files over 300 lines get split — none of the files this plan touches are near that threshold
  after these changes.

---

## Task 1: `tenancy.PortalInviteForCustomer`

**Files:**
- Modify: `tenancy/portal_invites.go`
- Test: `tenancy/portal_invites_dbtest_test.go` (new)

**Interfaces:**
- Consumes: existing `tenancy.ControlPlane` (`tenancy/controlplane.go`), existing
  `PortalInvite` struct, `portalInviteColumns`, `scanPortalInvite` (all in
  `tenancy/portal_invites.go`), existing `ErrPortalInviteNotFound`.
- Produces: `func (c *ControlPlane) PortalInviteForCustomer(ctx context.Context, tenantID, customerUUID string) (*PortalInvite, error)` — used by Task 2.

- [ ] **Step 1: Write the failing test**

Create `tenancy/portal_invites_dbtest_test.go`:

```go
//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPortalInviteForCustomer(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	mkAcceptedInvite := func(t *testing.T, customerUUID string) *PortalInvite {
		t.Helper()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		email := "customer-" + suffix + "@example.com"
		identity, err := cp.CreateIdentity(ctx, tenantID, email, "hash", "Test Customer", true)
		if err != nil {
			t.Fatalf("CreateIdentity: %v", err)
		}
		inv, err := cp.CreatePortalInvite(ctx, tenantID, identity.ID, email, "Test Customer",
			customerUUID, "tok-"+suffix, "", time.Now().Add(48*time.Hour))
		if err != nil {
			t.Fatalf("CreatePortalInvite: %v", err)
		}
		if _, err := cp.CreatePortalLink(ctx, identity.ID, tenantID); err != nil {
			t.Fatalf("CreatePortalLink: %v", err)
		}
		if err := cp.MarkPortalInviteAccepted(ctx, inv.ID); err != nil {
			t.Fatalf("MarkPortalInviteAccepted: %v", err)
		}
		got, err := cp.PortalInviteByToken(ctx, inv.Token)
		if err != nil {
			t.Fatalf("PortalInviteByToken (re-fetch accepted): %v", err)
		}
		return got
	}

	t.Run("no invite for this customer returns ErrPortalInviteNotFound", func(t *testing.T) {
		_, err := cp.PortalInviteForCustomer(ctx, tenantID, "00000000-0000-0000-0000-000000000001")
		if !errors.Is(err, ErrPortalInviteNotFound) {
			t.Fatalf("err = %v, want ErrPortalInviteNotFound", err)
		}
	})

	t.Run("pending (not yet accepted) invite is not returned", func(t *testing.T) {
		customerUUID := "00000000-0000-0000-0000-000000000002"
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		email := "pending-" + suffix + "@example.com"
		identity, err := cp.CreateIdentity(ctx, tenantID, email, "hash", "Pending Customer", true)
		if err != nil {
			t.Fatalf("CreateIdentity: %v", err)
		}
		if _, err := cp.CreatePortalInvite(ctx, tenantID, identity.ID, email, "Pending Customer",
			customerUUID, "tok-"+suffix, "", time.Now().Add(48*time.Hour)); err != nil {
			t.Fatalf("CreatePortalInvite: %v", err)
		}

		_, err = cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if !errors.Is(err, ErrPortalInviteNotFound) {
			t.Fatalf("err = %v, want ErrPortalInviteNotFound for a still-pending invite", err)
		}
	})

	t.Run("accepted invite is found and matches the identity", func(t *testing.T) {
		customerUUID := "00000000-0000-0000-0000-000000000003"
		accepted := mkAcceptedInvite(t, customerUUID)

		got, err := cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if err != nil {
			t.Fatalf("PortalInviteForCustomer: %v", err)
		}
		if got.IdentityID != accepted.IdentityID {
			t.Fatalf("IdentityID = %q, want %q", got.IdentityID, accepted.IdentityID)
		}
		if got.CustomerUUID != customerUUID {
			t.Fatalf("CustomerUUID = %q, want %q", got.CustomerUUID, customerUUID)
		}
		if got.Status != PortalInviteStatusAccepted {
			t.Fatalf("Status = %q, want %q", got.Status, PortalInviteStatusAccepted)
		}
	})

	t.Run("most recently accepted invite wins when a customer was re-invited", func(t *testing.T) {
		customerUUID := "00000000-0000-0000-0000-000000000004"
		_ = mkAcceptedInvite(t, customerUUID)
		time.Sleep(10 * time.Millisecond) // force a distinguishable accepted_at
		second := mkAcceptedInvite(t, customerUUID)

		got, err := cp.PortalInviteForCustomer(ctx, tenantID, customerUUID)
		if err != nil {
			t.Fatalf("PortalInviteForCustomer: %v", err)
		}
		if got.IdentityID != second.IdentityID {
			t.Fatalf("IdentityID = %q, want the more recently accepted identity %q", got.IdentityID, second.IdentityID)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags dbtest ./tenancy/... -run TestPortalInviteForCustomer -v`
Expected: FAIL — `cp.PortalInviteForCustomer undefined (type *ControlPlane has no field or method PortalInviteForCustomer)`

(Requires `TEST_DATABASE_URL` set to a control-plane-schema-migrated Postgres instance — see
`CLAUDE.md`'s "Common Commands". If unset, this and every other `dbtest` step in this plan
skips cleanly instead of failing; run `go build ./...` and `go vet ./...` as the fallback
verification for those steps.)

- [ ] **Step 3: Write minimal implementation**

In `tenancy/portal_invites.go`, add after `PendingPortalInviteByEmail` (so invite-lookup
methods stay grouped):

```go
// PortalInviteForCustomer returns the most recently accepted portal invite
// linking customerUUID (a tenant-DB customer.customer_uuid) to a
// control-plane identity, or ErrPortalInviteNotFound if this customer has no
// accepted portal invite in this tenant. The caller must additionally check
// PortalLinkActive before treating the result as currently notifiable --
// revoking access updates identity_tenants.status, not portal_invites.status,
// so a revoked customer's invite still shows as "accepted" here.
func (c *ControlPlane) PortalInviteForCustomer(ctx context.Context, tenantID, customerUUID string) (*PortalInvite, error) {
	q := `SELECT ` + portalInviteColumns + `
		FROM portal_invites
		WHERE tenant_id = $1 AND customer_uuid = $2 AND status = 'accepted'
		ORDER BY accepted_at DESC LIMIT 1`
	return scanPortalInvite(c.pool.QueryRow(ctx, q, tenantID, customerUUID))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags dbtest ./tenancy/... -run TestPortalInviteForCustomer -v`
Expected: PASS (all 4 subtests)

- [ ] **Step 5: Commit**

```bash
git add tenancy/portal_invites.go tenancy/portal_invites_dbtest_test.go
git commit -m "feat(tenancy): add PortalInviteForCustomer lookup"
```

---

## Task 2: `notifyCustomerApproved` helper + wire into Invoice Approve

**Files:**
- Create: `controllers/customer_notify.go`
- Modify: `controllers/invoice.go`
- Modify: `main.go`
- Test: `controllers/customer_notify_dbtest_test.go` (new)

**Interfaces:**
- Consumes: `tenancy.ControlPlane` (Task 1's `PortalInviteForCustomer`, existing
  `PortalLinkActive`, `IdentityByID`, `TenantFromContext`), `services.SendNotification` /
  `services.NotificationRequest` / `services.RecipientTarget` (`services/notify.go`, unchanged),
  `invoice.Invoice{Number, StatusCode, Customer CustomerRef{ID, Name}}` (`invoice/types.go`).
- Produces: `func notifyCustomerApproved(ctx context.Context, cp *tenancy.ControlPlane, actorIdentityID, customerUUID, resource, displayName, number, recordUUID string)` (package `controllers`) — used by every later task in this plan. `InvoiceOps` gains a `cp *tenancy.ControlPlane` field; `NewInvoiceOps` now takes `cp *tenancy.ControlPlane`.

- [ ] **Step 1: Write the failing test**

This tests `notifyCustomerApproved` directly (not through `InvoiceOps.Approve`'s full HTTP
stack) — the "did Approve finalize correctly" question is already covered by this repo's
existing invoice approval tests; what's new here is purely the customer-resolution and
notify-send logic, so that's what gets dbtest coverage, isolated from needing to also stand up
an invoice + its approver rows. It still runs through the real
`tenancy.Resolver.Middleware` (the only way outside package `tenancy` to populate a context
`tenancy.TenantFromContext` can read — see `document_send_dbtest_test.go`'s own note on why
this needs both `TEST_CP_DATABASE_URL` and `TEST_DATABASE_URL`), and reuses that same file's
already-defined `docSendTestControlPlane`/`docSendTestTenantDSN` helpers (same package,
`//go:build dbtest`, so no redeclaration).

Create `controllers/customer_notify_dbtest_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags dbtest ./controllers/... -run TestNotifyCustomerApproved_DB -v`
Expected: FAIL — compile error (`notifyCustomerApproved` undefined), since Step 3 hasn't run yet.

- [ ] **Step 3: Write minimal implementation**

Create `controllers/customer_notify.go`:

```go
// controllers/customer_notify.go
package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// notifyCustomerApproved best-effort-notifies a document's customer, via
// their portal login if they have an active one, that it's been approved.
// No-ops silently (logs and returns) if the customer has no accepted portal
// invite, or their portal access has since been revoked -- this is not an
// error, just nothing to do. Never blocks or fails the caller's HTTP
// response: sending this notification is not part of the approve handler's
// contract, matching every Notify* failure semantics elsewhere in this
// codebase (see approvalchain/notify.go).
//
// resource is the workflows.key / route segment (e.g. "invoice") -- it is
// used verbatim both as the notification's Resource/EventType prefix and as
// the /sales/<resource>/<recordUUID> link, since portal and staff sessions
// share the exact same detail routes (see
// docs/superpowers/specs/2026-09-10-customer-portal-approval-notifications-design.md
// §2).
func notifyCustomerApproved(ctx context.Context, cp *tenancy.ControlPlane, actorIdentityID, customerUUID, resource, displayName, number, recordUUID string) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "customer notify: no tenant in context, skipping", "resource", resource, "recordId", recordUUID)
		return
	}
	invite, err := cp.PortalInviteForCustomer(ctx, tenant.ID, customerUUID)
	if errors.Is(err, tenancy.ErrPortalInviteNotFound) {
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: resolve portal invite failed", "resource", resource, "recordId", recordUUID, "error", err)
		return
	}
	active, err := cp.PortalLinkActive(ctx, invite.IdentityID, tenant.ID)
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: check portal link active failed", "resource", resource, "recordId", recordUUID, "error", err)
		return
	}
	if !active {
		return
	}
	identity, err := cp.IdentityByID(ctx, invite.IdentityID)
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: resolve identity failed", "resource", resource, "recordId", recordUUID, "error", err)
		return
	}
	err = services.SendNotification(ctx, services.NotificationRequest{
		TenantID:    tenant.ID,
		Recipients:  []services.RecipientTarget{{UserID: identity.ID, Email: identity.Email}},
		ActorUserID: actorIdentityID,
		EventType:   resource + ".customer_approved",
		Resource:    resource,
		ResourceID:  recordUUID,
		Title:       fmt.Sprintf("Your %s %s has been approved", displayName, number),
		Body:        "Approved.",
		Link:        fmt.Sprintf("/sales/%s/%s", resource, recordUUID),
		Channels:    []string{"email"},
	})
	if err != nil {
		slog.ErrorContext(ctx, "customer notify: send notification failed", "resource", resource, "recordId", recordUUID, "error", err)
	}
}
```

In `controllers/invoice.go`, change:

```go
type InvoiceOps struct{}

func NewInvoiceOps() *InvoiceOps { return &InvoiceOps{} }
```

to:

```go
type InvoiceOps struct {
	cp *tenancy.ControlPlane
}

func NewInvoiceOps(cp *tenancy.ControlPlane) *InvoiceOps { return &InvoiceOps{cp: cp} }
```

(`tenancy` is already imported in this file for `tenancy.PoolFromContext` — no new import
needed.)

In the same file, change `Approve`:

```go
func (h *InvoiceOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authInvoiceByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		invoiceFail(w, err, "Failed to approve invoice.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	inv, err := invoice.Approve(r.Context(), pool, uuid, empID, isSuperAdmin)
	if err != nil {
		if errors.Is(err, invoice.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		invoiceFail(w, err, "Failed to approve invoice.")
		return
	}
	auditInvoice(r, pool, empID, "approve", uuid, nil, inv)
	if inv.StatusCode == "APPV" {
		notifyCustomerApproved(r.Context(), h.cp, identityID, inv.Customer.ID, "invoice", "Invoice", inv.Number, uuid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": inv})
}
```

(Only the two lines from `if inv.StatusCode == "APPV"` are new; everything else in the
function is unchanged — shown in full so the diff is unambiguous.)

In `main.go`, change:

```go
		invOps := controllers.NewInvoiceOps()
```

to:

```go
		invOps := controllers.NewInvoiceOps(cp)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags dbtest ./controllers/... -run TestNotifyCustomerApproved_DB -v`
Expected: PASS (all 3 subtests)

Also run, to confirm nothing else broke:

Run: `go build ./... && go vet ./...`
Expected: no output (success)

- [ ] **Step 5: Commit**

```bash
git add controllers/customer_notify.go controllers/invoice.go controllers/customer_notify_dbtest_test.go main.go
git commit -m "feat(controllers): notify customer on invoice approval finalize"
```

---

## Task 3: Wire Sales Order

**Files:**
- Modify: `controllers/salesorder.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `notifyCustomerApproved` (Task 2), `salesorder.Order{Number, StatusCode, Customer CustomerRef{ID}}` (`salesorder/types.go`).
- Produces: nothing new consumed by later tasks — this and Tasks 4/5 are independent siblings of Task 2.

- [ ] **Step 1: Write the failing test**

This task has no new dbtest coverage of its own — Task 2's spec review (§6 of the design doc)
scopes dbtest coverage to one reference module (invoice); Sales Order gets the identical
wiring pattern, verified by build/vet plus the unit-level checks below. Add a small
table-driven test asserting the finalize-detection condition alongside the module's existing
tests, in `controllers/salesorder_test.go` (create if it doesn't exist as a non-dbtest file;
check first — if it exists, add this test function to it rather than creating a duplicate
file):

```go
package controllers

import "testing"

// TestSalesOrderApproveFinalizeCondition documents and pins the exact
// condition this package's Approve handler uses to decide whether an
// approval call just finalized (customer-notify-worthy) versus recorded a
// partial sign-off (not worthy) -- "APPV" is the sales_order gate's
// TargetStatusCode (approvalchain/registry.go's "sales_order" entry).
func TestSalesOrderApproveFinalizeCondition(t *testing.T) {
	cases := []struct {
		name       string
		statusCode string
		wantNotify bool
	}{
		{"still gated after a partial sign-off", "PAPV", false},
		{"finalized this call", "APPV", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.statusCode == "APPV"
			if got != tc.wantNotify {
				t.Errorf("statusCode %q: got notify=%v, want %v", tc.statusCode, got, tc.wantNotify)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./controllers/... -run TestSalesOrderApproveFinalizeCondition -v`
Expected: PASS immediately (this test doesn't depend on the not-yet-written handler change —
it pins the condition in isolation). This is expected: proceed to Step 3, then re-run in Step 4
to confirm the real handler now uses the same condition.

- [ ] **Step 3: Write minimal implementation**

In `controllers/salesorder.go`, change:

```go
type SalesOrderOps struct{}

func NewSalesOrderOps() *SalesOrderOps { return &SalesOrderOps{} }
```

to:

```go
type SalesOrderOps struct {
	cp *tenancy.ControlPlane
}

func NewSalesOrderOps(cp *tenancy.ControlPlane) *SalesOrderOps { return &SalesOrderOps{cp: cp} }
```

(Confirm `tenancy` is already imported in this file before adding — it is, for
`tenancy.PoolFromContext` used elsewhere in the same file.)

Change `Approve`:

```go
func (h *SalesOrderOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		soFail(w, err, "Failed to approve sales order.")
		return
	}
	order, err := salesorder.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		if errors.Is(err, salesorder.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		soFail(w, err, "Failed to approve sales order.")
		return
	}
	auditSO(r, pool, identityID, "approve", uuid, nil, order)
	if order.StatusCode == "APPV" {
		notifyCustomerApproved(r.Context(), h.cp, identityID, order.Customer.ID, "sales_order", "Sales Order", order.Number, uuid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "salesOrder": order})
}
```

In `main.go`, change:

```go
		so := controllers.NewSalesOrderOps()
```

to:

```go
		so := controllers.NewSalesOrderOps(cp)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./... && go vet ./... && go test ./controllers/... -run TestSalesOrderApproveFinalizeCondition -v`
Expected: build/vet clean, test PASS.

- [ ] **Step 5: Commit**

```bash
git add controllers/salesorder.go controllers/salesorder_test.go main.go
git commit -m "feat(controllers): notify customer on sales order approval finalize"
```

---

## Task 4: Wire Payment

**Files:**
- Modify: `controllers/payment.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `notifyCustomerApproved` (Task 2), `payment.Payment{Number, StatusCode, Customer CustomerRef{ID}}` (`payment/types.go`).

- [ ] **Step 1: Write the failing test**

Same rationale as Task 3 — no new dbtest coverage, add the finalize-condition pin to
`controllers/payment_test.go` (it already exists — `controllers/payment_test.go` was seen in
the earlier codebase audit; add this function to it):

```go
func TestPaymentApproveFinalizeCondition(t *testing.T) {
	cases := []struct {
		name       string
		statusCode string
		wantNotify bool
	}{
		{"still gated after a partial sign-off", "PEND", false},
		{"finalized this call", "APPV", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.statusCode == "APPV"
			if got != tc.wantNotify {
				t.Errorf("statusCode %q: got notify=%v, want %v", tc.statusCode, got, tc.wantNotify)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./controllers/... -run TestPaymentApproveFinalizeCondition -v`
Expected: PASS immediately (same self-contained-condition note as Task 3).

- [ ] **Step 3: Write minimal implementation**

In `controllers/payment.go`, change:

```go
type PaymentOps struct{}

func NewPaymentOps() *PaymentOps { return &PaymentOps{} }
```

to:

```go
type PaymentOps struct {
	cp *tenancy.ControlPlane
}

func NewPaymentOps(cp *tenancy.ControlPlane) *PaymentOps { return &PaymentOps{cp: cp} }
```

Change `Approve`:

```go
func (h *PaymentOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		paymentFail(w, err, "Failed to approve payment.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Approve(r.Context(), pool, uuid, empID, isSuperAdmin)
	if err != nil {
		if errors.Is(err, payment.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		paymentFail(w, err, "Failed to approve payment.")
		return
	}
	auditPayment(r, pool, empID, "approve", uuid, nil, p)
	if p.StatusCode == "APPV" {
		notifyCustomerApproved(r.Context(), h.cp, identityID, p.Customer.ID, "payment", "Payment", p.Number, uuid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}
```

In `main.go`, change:

```go
		payOps := controllers.NewPaymentOps()
```

to:

```go
		payOps := controllers.NewPaymentOps(cp)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./... && go vet ./... && go test ./controllers/... -run TestPaymentApproveFinalizeCondition -v`
Expected: build/vet clean, test PASS.

- [ ] **Step 5: Commit**

```bash
git add controllers/payment.go main.go
git commit -m "feat(controllers): notify customer on payment approval finalize"
```

---

## Task 5: Wire Refund

**Files:**
- Modify: `controllers/refund.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `notifyCustomerApproved` (Task 2), `refund.Refund{Number, StatusCode, Customer CustomerRef{ID}}` (`refund/types.go`).

- [ ] **Step 1: Write the failing test**

Same rationale as Tasks 3/4, added to `controllers/refund_test.go` (check it exists first —
if not, this is the one new small test file for this task):

```go
func TestRefundApproveFinalizeCondition(t *testing.T) {
	cases := []struct {
		name       string
		statusCode string
		wantNotify bool
	}{
		{"still gated after a partial sign-off", "PEND", false},
		{"finalized this call", "APPV", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.statusCode == "APPV"
			if got != tc.wantNotify {
				t.Errorf("statusCode %q: got notify=%v, want %v", tc.statusCode, got, tc.wantNotify)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./controllers/... -run TestRefundApproveFinalizeCondition -v`
Expected: PASS immediately (same self-contained-condition note as Tasks 3/4).

- [ ] **Step 3: Write minimal implementation**

In `controllers/refund.go`, change:

```go
type RefundOps struct{}

func NewRefundOps() *RefundOps { return &RefundOps{} }
```

to:

```go
type RefundOps struct {
	cp *tenancy.ControlPlane
}

func NewRefundOps(cp *tenancy.ControlPlane) *RefundOps { return &RefundOps{cp: cp} }
```

Change `Approve`:

```go
func (h *RefundOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	// ActionApprove, not ActionTransition -- approving a refund (PEND->APPV)
	// is what authorizes it to draw down a payment or credit memo, a
	// deliberately distinct capability from moving the record around (see
	// authz/catalog.go).
	pool, identityID, _, ok := h.authRefundByUUID(w, r, uuid, authz.ActionApprove)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		refundFail(w, err, "Failed to approve refund.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	rf, err := refund.Approve(r.Context(), pool, uuid, empID, isSuperAdmin)
	if err != nil {
		if errors.Is(err, refund.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		refundFail(w, err, "Failed to approve refund.")
		return
	}
	auditRefund(r, pool, empID, "approve", uuid, nil, rf)
	if rf.StatusCode == "APPV" {
		notifyCustomerApproved(r.Context(), h.cp, identityID, rf.Customer.ID, "refund", "Refund", rf.Number, uuid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "refund": rf})
}
```

In `main.go`, change:

```go
		rfndOps := controllers.NewRefundOps()
```

to:

```go
		rfndOps := controllers.NewRefundOps(cp)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./... && go vet ./... && go test ./controllers/... -run TestRefundApproveFinalizeCondition -v`
Expected: build/vet clean, test PASS.

Then run the full non-dbtest suite once as a final sanity check for all of Tasks 2-5 together:

Run: `go build ./... && go vet ./... && go test ./...`
Expected: no failures (dbtest-tagged tests are skipped without `TEST_DATABASE_URL`, which is
expected and not a failure).

- [ ] **Step 5: Commit**

```bash
git add controllers/refund.go controllers/refund_test.go main.go
git commit -m "feat(controllers): notify customer on refund approval finalize"
```

---

## Task 6: Un-hide the notification bell for portal sessions

**Files:**
- Modify: `src/components/NotificationBell.tsx` (repo: `StoneSuite-WebUI`)
- Test: `src/components/NotificationBell.test.tsx` (new)

**Interfaces:**
- Consumes: existing `notificationService` (`src/services/notificationService.ts`, unchanged),
  existing `useAuthStore` (`src/store/useAuthStore.ts`, unchanged).
- Produces: nothing consumed by other tasks — this is the final, independent task.

- [ ] **Step 1: Write the failing test**

Create `src/components/NotificationBell.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import type { ReactNode } from 'react';

vi.mock('@/store/useAuthStore', () => ({ useAuthStore: vi.fn() }));
vi.mock('@/services/notificationService', () => ({
  notificationService: {
    unreadCount: vi.fn(),
    list: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
  },
}));

import { NotificationBell } from './NotificationBell';
import { useAuthStore } from '@/store/useAuthStore';
import { notificationService } from '@/services/notificationService';

function mockAuth(state: { isAuthenticated: boolean; kind?: 'portal' }) {
  vi.mocked(useAuthStore).mockImplementation((selector) =>
    (selector as (s: unknown) => unknown)(state),
  );
}

function renderBell() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
  return render(<NotificationBell />, { wrapper });
}

beforeEach(() => vi.clearAllMocks());

describe('NotificationBell', () => {
  it('renders for a portal (customer) session, not just staff', async () => {
    mockAuth({ isAuthenticated: true, kind: 'portal' });
    vi.mocked(notificationService.unreadCount).mockResolvedValue(2);
    renderBell();

    expect(await screen.findByRole('button', { name: /notifications/i })).toBeInTheDocument();
  });

  it('still renders for a staff session', async () => {
    mockAuth({ isAuthenticated: true, kind: undefined });
    vi.mocked(notificationService.unreadCount).mockResolvedValue(0);
    renderBell();

    expect(await screen.findByRole('button', { name: /notifications/i })).toBeInTheDocument();
  });

  it('renders nothing when unauthenticated', () => {
    mockAuth({ isAuthenticated: false, kind: undefined });
    const { container } = renderBell();

    expect(container).toBeEmptyDOMElement();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npm run test -- NotificationBell.test.tsx`
Expected: FAIL — the first test ("renders for a portal session") fails because the component
still returns `null` for a portal session.

- [ ] **Step 3: Write minimal implementation**

In `src/components/NotificationBell.tsx`, change:

```ts
export function NotificationBell() {
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated);
  const isCustomer = useAuthStore((s) => s.kind === 'portal');
  const queryClient = useQueryClient();

  // Customer-portal sessions have no StoneSuite users.id behind them for
  // most flows (see docs/superpowers/specs/2026-08-28-notify-customer-...
  // -design.md D1/D2) -- stonesuite-notify's user-facing API requires a real
  // recipientUserId, so there is nothing this bell could ever show a
  // customer. Hide it there rather than polling for an always-empty list.
  const enabled = isAuthenticated && !isCustomer;
```

to:

```ts
export function NotificationBell() {
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated);
  const queryClient = useQueryClient();

  const enabled = isAuthenticated;
```

(`isCustomer` is now unused and removed entirely — it had no other use in this file.)

- [ ] **Step 4: Run test to verify it passes**

Run: `npm run test -- NotificationBell.test.tsx`
Expected: PASS (all 3 tests)

Then run the broader check:

Run: `npx tsc -b && npx eslint src/components/NotificationBell.tsx src/components/NotificationBell.test.tsx`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add src/components/NotificationBell.tsx src/components/NotificationBell.test.tsx
git commit -m "feat(notifications): show the bell for customer portal sessions"
```

---

## Post-implementation checks (run once, after Task 6)

- [ ] Dispatch `tenancy-security-reviewer` against the full branch diff — this plan adds
  control-plane lookups reachable from tenant-scoped request handlers (Tasks 2-5), which is
  exactly the auth-boundary-adjacent surface that agent is scoped to check.
- [ ] Dispatch `module-drift-checker` for `invoice`, `salesorder`, `payment`, `refund` — Tasks
  2-5 touch each module's controller in a structurally identical way; this catches any
  copy-paste drift between the four.
- [ ] Manual browser verification (per this session's existing `run`/browser-preview workflow):
  log in to the portal as a seeded customer with an accepted invite, have staff approve one of
  their documents, confirm the bell shows the new notification and clicking it navigates to
  the correct `/sales/<resource>/<uuid>` route.
