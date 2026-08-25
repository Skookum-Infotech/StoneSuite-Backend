package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// main.go has two routers, not one.
//
// ServeMux decides which handler serves a path. Before that, the CORS handler
// tests the path against a hardcoded allowlist of prefixes and 404s anything
// outside it. A route can therefore be registered perfectly and still be dead
// in production — there is no compile error, no failing test, and nothing in a
// diff to notice, because the two live hundreds of lines apart.
//
// This has already happened twice: /api/portal/* when the customer portal
// landed, and /api/customer/* which shipped dead in PR #140 and stayed that way
// across several releases.
//
// The check runs the docs generator, which parses both the route table and the
// allowlist out of main.go with go/ast and reports any route the allowlist
// rejects. Keeping the parsing in one place means this test and the published
// API reference can never disagree about what is reachable.
func TestEveryRegisteredRouteIsReachable(t *testing.T) {
	out, err := exec.Command("go", "run", "./cmd/gen-apidocs", "-out", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("gen-apidocs failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "UNREACHABLE") {
		t.Errorf(`registered routes are unreachable at runtime.

The prefix allowlist in main.go's corsHandler rejects them before ServeMux is
consulted, so they return 404 in production.

Fix: add the missing prefix to that condition in main.go.

%s`, out)
	}
}

// The generated API docs are the published contract. If someone adds a route
// and does not regenerate, the docs silently understate the API surface — which
// is the failure mode the generator exists to prevent.
func TestGeneratedAPIDocsAreCurrent(t *testing.T) {
	out, err := exec.Command("go", "run", "./cmd/gen-apidocs", "-check").CombinedOutput()
	if err == nil {
		return
	}
	t.Errorf(`the generated API docs are out of date.

Run this and commit the result:

    go run ./cmd/gen-apidocs

%s`, out)
}

// Every unauthenticated endpoint is a deliberate decision, so the set of them
// is pinned here. Adding one is fine — but it must be a conscious edit to this
// list, reviewed as such, rather than something that slips in with a feature.
//
// A route appearing here that should not be public is a security bug; a route
// missing from here that is public means someone widened the attack surface
// without saying so.
func TestUnauthenticatedEndpointsArePinned(t *testing.T) {
	want := map[string]bool{
		// Liveness / readiness / metrics.
		"ANY /api": true, "GET /api/healthz": true, "GET /api/readyz": true,
		"GET /api/metrics": true,

		// Staff credential endpoints.
		"ANY /api/auth/tenant-login": true, "POST /api/auth/identify": true,
		"POST /api/auth/refresh": true, "POST /api/auth/logout": true,
		"POST /api/auth/forgot-password": true, "POST /api/auth/reset-password": true,
		"GET /api/auth/reset-password/{token}": true,

		// SAML: the IdP and the browser reach these unauthenticated by design.
		"POST /api/auth/saml/discover": true, "POST /api/auth/saml/exchange": true,
		"POST /api/auth/saml/{provider}/acs":            true,
		"GET /api/auth/saml/{provider}/initiate":        true,
		"GET /api/auth/saml/{provider}/logout-response": true,
		"GET /api/auth/saml/{provider}/metadata":        true,
		"GET /api/auth/saml/{provider}/sp-info":         true,

		// Public tenant onboarding and workspace-user invitations.
		"ANY /api/onboarding/form-schema": true, "ANY /api/onboarding/apply": true,
		"ANY /api/onboarding/apply/": true, "ANY /api/onboarding/set-password": true,
		"ANY /api/onboarding/set-password/":       true,
		"POST /api/onboarding/user-invite/accept": true,
		"GET /api/onboarding/user-invite/{token}": true,

		// One-time platform bootstrap.
		"GET /api/platform/setup/status": true, "POST /api/platform/activate": true,

		// Customer-portal credential endpoints.
		"POST /api/portal/auth/login": true, "POST /api/portal/auth/logout": true,
		"POST /api/portal/auth/refresh":               true,
		"GET /api/portal/auth/invite/{token}":         true,
		"POST /api/portal/auth/accept-invite":         true,
		"POST /api/portal/auth/forgot-password":       true,
		"POST /api/portal/auth/reset-password":        true,
		"GET /api/portal/auth/reset-password/{token}": true,

		// Second customer surface (PR #140) — see docs/architecture-overview.md.
		"POST /api/customer/auth/login":         true,
		"POST /api/customer/auth/accept-invite": true,
	}

	got, err := unauthenticatedRoutes()
	if err != nil {
		t.Fatalf("read routes: %v", err)
	}

	var added, removed []string
	for r := range got {
		if !want[r] {
			added = append(added, r)
		}
	}
	for r := range want {
		if !got[r] {
			removed = append(removed, r)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)

	if len(added) > 0 {
		t.Errorf(`new UNAUTHENTICATED endpoint(s):

%s

Anyone on the internet can call these. If that is intended, add them to the
list in this test so the change is reviewed deliberately.`, strings.Join(added, "\n  "))
	}
	if len(removed) > 0 {
		t.Errorf(`endpoint(s) no longer public (or renamed/removed):

  %s

If deliberate, drop them from the list in this test.`, strings.Join(removed, "\n  "))
	}
}

// unauthenticatedRoutes asks the generator which endpoints need no credential.
func unauthenticatedRoutes() (map[string]bool, error) {
	out, err := exec.Command("go", "run", "./cmd/gen-apidocs", "-out", "/dev/null", "-list-public").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	got := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			got[line] = true
		}
	}
	return got, nil
}

// The generated docs are committed and compared against a fresh run in CI, so
// generation must be a pure function of main.go. It has not always been: Go
// randomises map iteration and sort.Slice is not stable, so two auth postures
// with equal counts once swapped between runs and made the drift check fail
// roughly two times in five.
//
// A flaky guard is worse than no guard — people learn to re-run CI until it
// goes green, and stop reading it.
func TestDocGenerationIsDeterministic(t *testing.T) {
	const runs = 5
	first := map[string]string{}

	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		out, err := exec.Command("go", "run", "./cmd/gen-apidocs", "-out", dir).CombinedOutput()
		if err != nil {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
		for _, name := range []string{
			"api-reference.md", "openapi.json", "StoneSuite.postman_collection.json",
		} {
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("run %d: read %s: %v", i, name, err)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(body))
			if i == 0 {
				first[name] = sum
				continue
			}
			if sum != first[name] {
				t.Errorf("%s differs between identical runs (run 0 vs run %d).\n"+
					"Generation must be deterministic: check for map iteration "+
					"feeding output, or an unstable sort with no tiebreak.", name, i)
			}
		}
	}
}
