//go:build dbtest

package tenancy

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newCPTestControlPlane connects to the control-plane-schema test database.
// Skips cleanly when TEST_CP_DATABASE_URL is unset (this package's dbtest
// suite runs against the control-plane schema, not the per-tenant schema
// TEST_DATABASE_URL points at -- see ai/cphelp_test.go for the same split).
func newCPTestControlPlane(t *testing.T) *ControlPlane {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return &ControlPlane{pool: pool}
}

// seedTestTenant inserts a minimal tenant row and returns its id, for tests
// that need a valid tenants.id to satisfy identities' FK.
func seedTestTenant(t *testing.T, cp *ControlPlane) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(context.Background(), "test-tenant-"+suffix, "Test Tenant", false)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return tenant.ID
}

func TestCreateSSOIdentity(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	email := fmt.Sprintf("saml-user-%d@example.com", time.Now().UnixNano())
	// idx_identities_sso is a UNIQUE index on (sso_provider, sso_subject);
	// tie the subject to tenantID (a fresh UUID per run) so repeated local
	// runs against a persistent test DB never collide with a prior run's row.
	ssoSubject := "subject-" + tenantID

	id, err := cp.CreateSSOIdentity(ctx, tenantID, email, "SAML User", "entra", ssoSubject)
	if err != nil {
		t.Fatalf("CreateSSOIdentity: %v", err)
	}
	if id.Email != email {
		t.Errorf("Email = %q, want %q", id.Email, email)
	}
	if !id.EmailVerified {
		t.Error("EmailVerified = false, want true (IdP already vouched for the address)")
	}
	if id.PasswordHash != "" {
		t.Errorf("PasswordHash = %q, want empty (no password on the SSO path)", id.PasswordHash)
	}
	if id.SSOProvider != "entra" {
		t.Errorf("SSOProvider = %q, want entra", id.SSOProvider)
	}
	if id.SSOSubject != ssoSubject {
		t.Errorf("SSOSubject = %q, want %q", id.SSOSubject, ssoSubject)
	}
}

func TestLinkSSOIdentity(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	email := fmt.Sprintf("link-user-%d@example.com", time.Now().UnixNano())
	// idx_identities_sso is a UNIQUE index on (sso_provider, sso_subject);
	// tie the subject to tenantID (a fresh UUID per run) so repeated local
	// runs against a persistent test DB never collide with a prior run's row.
	ssoSubject := "subject-" + tenantID
	sessionIndex := "session-" + tenantID

	created, err := cp.CreateIdentity(ctx, tenantID, email, "hashed-pw", "Password User", true)
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if created.SSOProvider != "" || created.SSOSubject != "" {
		t.Fatalf("newly created password identity should start with no SSO link, got provider=%q subject=%q",
			created.SSOProvider, created.SSOSubject)
	}

	if err := cp.LinkSSOIdentity(ctx, created.ID, "cognito", ssoSubject, sessionIndex); err != nil {
		t.Fatalf("LinkSSOIdentity: %v", err)
	}
	linked, err := cp.IdentityByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("IdentityByID: %v", err)
	}
	if linked.SSOProvider != "cognito" || linked.SSOSubject != ssoSubject {
		t.Fatalf("after link: provider=%q subject=%q, want cognito/%s", linked.SSOProvider, linked.SSOSubject, ssoSubject)
	}
	if linked.SSOSessionIndex != sessionIndex {
		t.Errorf("SSOSessionIndex = %q, want %q", linked.SSOSessionIndex, sessionIndex)
	}
	// The password hash must survive linking -- LinkSSOIdentity only touches
	// the sso_provider/sso_subject/sso_session_index columns.
	if linked.PasswordHash != created.PasswordHash {
		t.Errorf("PasswordHash changed after LinkSSOIdentity: got %q, want %q", linked.PasswordHash, created.PasswordHash)
	}

	// Idempotent: a repeat call with the same values must not error and must
	// leave the row in the same state (simulates a repeat SAML login).
	if err := cp.LinkSSOIdentity(ctx, created.ID, "cognito", ssoSubject, sessionIndex); err != nil {
		t.Fatalf("LinkSSOIdentity (repeat call): %v", err)
	}
	relinked, err := cp.IdentityByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("IdentityByID (after repeat link): %v", err)
	}
	if relinked.SSOProvider != "cognito" || relinked.SSOSubject != ssoSubject {
		t.Fatalf("after repeat link: provider=%q subject=%q, want cognito/%s", relinked.SSOProvider, relinked.SSOSubject, ssoSubject)
	}
	if relinked.SSOSessionIndex != sessionIndex {
		t.Errorf("SSOSessionIndex after repeat link = %q, want %q", relinked.SSOSessionIndex, sessionIndex)
	}

	// A changed session index (e.g. a second, distinct login) must overwrite
	// the stored value -- LinkSSOIdentity is last-write-wins, not append-only.
	newSessionIndex := sessionIndex + "-2"
	if err := cp.LinkSSOIdentity(ctx, created.ID, "cognito", ssoSubject, newSessionIndex); err != nil {
		t.Fatalf("LinkSSOIdentity (session index change): %v", err)
	}
	resessioned, err := cp.IdentityByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("IdentityByID (after session index change): %v", err)
	}
	if resessioned.SSOSessionIndex != newSessionIndex {
		t.Errorf("SSOSessionIndex after change = %q, want %q", resessioned.SSOSessionIndex, newSessionIndex)
	}
}
