//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// bindings are the two ways an IdP can deliver a LogoutRequest; every scenario
// that is about the request itself runs over both.
var bindings = []struct {
	name  string
	build func(t *testing.T, m sloMsg, idp *sloIdP) *http.Request
}{
	{"HTTP-POST", func(t *testing.T, m sloMsg, idp *sloIdP) *http.Request { return m.post(t, idp) }},
	{"HTTP-Redirect", func(t *testing.T, m sloMsg, idp *sloIdP) *http.Request { return m.redirect(t, idp) }},
}

// answeredIdP asserts the response is a redirect back to the IdP's logout
// endpoint carrying a LogoutResponse, with the request's RelayState.
func answeredIdP(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusFound, rr.Code, rr.Body.String())
	loc, err := url.Parse(rr.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "idp.example.test", loc.Host)
	assert.NotEmpty(t, loc.Query().Get("SAMLResponse"))
	assert.Equal(t, "/home", loc.Query().Get("RelayState"))
}

func TestSLO_IdPInitiated_EndsTheUsersSessions_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	for _, b := range bindings {
		t.Run(b.name, func(t *testing.T) {
			w := newSLOWorld(t, cp, true, true)
			target, bystander := w.user("alice"), w.user("bob")
			second, err := cp.CreateSession(context.Background(), tenancy.NewSessionInput{IdentityID: target.identity.ID, UserAgent: "phone"})
			require.NoError(t, err)

			rr := w.send(b.build(t, sloMsg{nameID: target.subject}, w.idp))

			answeredIdP(t, rr)
			assert.True(t, w.ended(target.session), "the named user's session must end")
			assert.True(t, w.ended(second), "so must every other session of theirs")
			assert.False(t, w.ended(bystander.session), "another user in the same workspace is untouched")
			assertAuthCookiesCleared(t, rr)
		})
	}
}

func TestSLO_RefusesBadRequests_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	other := newSLOIdP(t)
	tests := []struct {
		name   string
		msg    sloMsg
		reason string
	}{
		{"unsigned", sloMsg{unsigned: true}, "not_signed"},
		{"signed by someone else's key", sloMsg{signer: other}, "bad_signature"},
		{"wrong destination", sloMsg{destination: "https://elsewhere.example.test/slo"}, "wrong_target"},
		{"too old", sloMsg{issuedAt: time.Now().Add(-time.Hour)}, "expired"},
	}
	for _, b := range bindings {
		for _, tc := range tests {
			t.Run(b.name+"/"+tc.name, func(t *testing.T) {
				logs := captureSecurityLog(t)
				w := newSLOWorld(t, cp, true, true)
				alice := w.user("alice")

				msg := tc.msg
				msg.nameID = alice.subject
				rr := w.send(b.build(t, msg, w.idp))

				assert.Equal(t, http.StatusBadRequest, rr.Code)
				assert.Contains(t, rr.Body.String(), sloRejectedMessage)
				assert.False(t, w.ended(alice.session), "a refused request must not sign anyone out")
				assert.Contains(t, logs.text(), "security_event=saml_slo_rejected")
				assert.Contains(t, logs.text(), "reason="+tc.reason)
			})
		}
	}
}

func TestSLO_UnknownOrDisabledIssuer_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	for _, b := range bindings {
		t.Run(b.name+"/unknown issuer", func(t *testing.T) {
			logs := captureSecurityLog(t)
			w := newSLOWorld(t, cp, true, true)
			alice := w.user("alice")
			stranger := newSLOIdP(t)

			rr := w.send(b.build(t, sloMsg{nameID: alice.subject}, stranger))

			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.False(t, w.ended(alice.session))
			assert.Contains(t, logs.text(), "reason=unknown_issuer")
		})
		t.Run(b.name+"/disabled config", func(t *testing.T) {
			w := newSLOWorld(t, cp, true, false)
			alice := w.user("alice")
			rr := w.send(b.build(t, sloMsg{nameID: alice.subject}, w.idp))
			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.False(t, w.ended(alice.session))
		})
	}
}

func TestSLO_ReplayedRequestDoesNothing_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	for _, b := range bindings {
		t.Run(b.name, func(t *testing.T) {
			logs := captureSecurityLog(t)
			w := newSLOWorld(t, cp, true, true)
			alice := w.user("alice")
			msg := sloMsg{nameID: alice.subject, id: fmt.Sprintf("_replayed-%s-%d", b.name, time.Now().UnixNano())}

			answeredIdP(t, w.send(b.build(t, msg, w.idp)))
			require.True(t, w.ended(alice.session))

			// Alice signs in again; an attacker replays the captured request.
			again, err := cp.CreateSession(context.Background(), tenancy.NewSessionInput{IdentityID: alice.identity.ID, UserAgent: "new"})
			require.NoError(t, err)
			rr := w.send(b.build(t, msg, w.idp))

			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.False(t, w.ended(again), "a replay must not end the new session")
			assert.Contains(t, logs.text(), "reason=replayed")
		})
	}
}

func TestSLO_UnknownPrincipalIsAnsweredLikeAKnownOne_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	for _, b := range bindings {
		t.Run(b.name, func(t *testing.T) {
			logs := captureSecurityLog(t)
			w := newSLOWorld(t, cp, true, true)
			alice := w.user("alice")

			rr := w.send(b.build(t, sloMsg{nameID: "nobody-by-that-name"}, w.idp))

			answeredIdP(t, rr) // same shape as a known user: no oracle for which names exist
			assert.False(t, w.ended(alice.session))
			assert.Contains(t, logs.text(), "security_event=saml_slo_unknown_principal")
		})
	}
}

func TestSLO_AnotherTenantsUserIsNeverReached_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	for _, b := range bindings {
		t.Run(b.name, func(t *testing.T) {
			attacker := newSLOWorld(t, cp, true, true) // its own tenant and IdP key
			victimWorld := newSLOWorld(t, cp, true, true)
			victim := victimWorld.user("bob")

			// The attacker's IdP signs a perfectly valid request naming bob, whose
			// account lives in a different tenant.
			rr := attacker.send(b.build(t, sloMsg{nameID: victim.subject}, attacker.idp))

			answeredIdP(t, rr) // unknown principal in THEIR tenant
			assert.False(t, victimWorld.ended(victim.session), "a request one tenant's IdP can sign must not end another tenant's session")
		})
	}
}

func TestSLO_WithoutAnIdPLogoutURLTheUserIsStillSignedOut_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	for _, b := range bindings {
		t.Run(b.name, func(t *testing.T) {
			w := newSLOWorld(t, cp, false, true)
			alice := w.user("alice")

			rr := w.send(b.build(t, sloMsg{nameID: alice.subject}, w.idp))

			require.Equal(t, http.StatusFound, rr.Code)
			assert.Contains(t, rr.Header().Get("Location"), "/auth/login?logged_out=true")
			assert.True(t, w.ended(alice.session))
		})
	}
}

func TestSLO_LogoutResponseTrafficKeepsLanding_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	w := newSLOWorld(t, cp, true, true)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/"+sloProvider+"/logout-response?SAMLResponse=abc", nil)
	req.SetPathValue("provider", sloProvider)
	rr := w.send(req)
	assert.Equal(t, http.StatusFound, rr.Code)
	assert.Contains(t, rr.Header().Get("Location"), "/auth/login?logged_out=true")
}

func TestSLO_SPInitiatedLogoutEndsTheServerSideSession_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	w := newSLOWorld(t, cp, true, true)
	alice := w.user("alice")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/"+sloProvider+"/logout", nil)
	req.SetPathValue("provider", sloProvider)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, middleware.UserContextPayload{
		ID: alice.identity.ID, TenantID: w.tenant.ID, SessionID: alice.session,
	}))
	rr := httptest.NewRecorder()
	w.h.Logout(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), `"slo_available":true`)
	assert.True(t, w.ended(alice.session), "logging out must end the session, not just clear cookies")
	assertAuthCookiesCleared(t, rr)
}

func TestClaimSAMLLogoutRequest_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	issuer := fmt.Sprintf("https://idp-%d.example.test/entity", time.Now().UnixNano())
	future := time.Now().Add(5 * time.Minute)

	claim := func(issuer, id string, expires time.Time) bool {
		t.Helper()
		first, err := cp.ClaimSAMLLogoutRequest(ctx, issuer, id, expires)
		require.NoError(t, err)
		return first
	}

	assert.True(t, claim(issuer, "_one", future), "the first claim wins")
	assert.False(t, claim(issuer, "_one", future), "a repeat of the same request is a replay")
	assert.True(t, claim(issuer, "_two", future), "a different request id is a different request")
	assert.True(t, claim(issuer+"/other", "_one", future), "ids are only unique per issuer")

	// A claim only has to outlive the request's validity window: once past it,
	// the row is pruned, so the table does not grow without bound.
	assert.True(t, claim(issuer, "_old", time.Now().Add(-time.Minute)))
	assert.True(t, claim(issuer, "_old", future), "an expired claim has been forgotten")
}
