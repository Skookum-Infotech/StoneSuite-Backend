//go:build dbtest

package controllers

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/aisettings"
	"stonesuite-backend/authz"
)

// TestAIAsk_GatedOnTenantToggle: with this tenant's own switch off, /ask
// refuses with the disabled 403 even though the platform switch is on.
func TestAIAsk_GatedOnTenantToggle(t *testing.T) {
	e := newAITestEnv(t, authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, aisettings.SetTenantEnabled(context.Background(), e.pool, false, "tester@example.com"))
	t.Cleanup(func() {
		_ = aisettings.SetTenantEnabled(context.Background(), e.pool, true, "tester@example.com")
	})

	rec := e.ask(t, `{"question":"what is Acme?"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"assistant_disabled"`)
}

// TestAIAsk_GatedOnPlatformToggle: with the platform master switch off,
// /ask refuses with the disabled 403 even though this tenant's own switch
// is on.
func TestAIAsk_GatedOnPlatformToggle(t *testing.T) {
	e := newAITestEnv(t, authz.Grant{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeAll})
	require.NoError(t, aisettings.SetPlatformEnabled(context.Background(), e.h.cpPool, false, "admin@example.com"))
	t.Cleanup(func() {
		_ = aisettings.SetPlatformEnabled(context.Background(), e.h.cpPool, true, "admin@example.com")
	})

	rec := e.ask(t, `{"question":"what is Acme?"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"assistant_disabled"`)
}

// TestAIAsk_BothTogglesOnIsNotGated: the default state (both on) must not
// change existing behaviour — /ask reaches the normal help-only 200.
func TestAIAsk_BothTogglesOnIsNotGated(t *testing.T) {
	e := newAITestEnv(t)
	rec := e.ask(t, `{"question":"how do I use StoneSuite?"}`)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}
