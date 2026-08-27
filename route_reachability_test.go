package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"stonesuite-backend/apidocs"
)

// These guards call the apidocs package directly rather than shelling out to
// `go run ./cmd/gen-apidocs`. An earlier version did shell out, which was both
// slower (eight subprocess compiles inside an already-parallel `go test -race
// ./...`) and weaker: a missing generator failed at runtime instead of being a
// compile error. It shipped exactly that way once — a .gitignore rule matched
// the cmd/gen-apidocs/ directory and silently excluded the generator's source
// from the commit, so CI ran tests against a generator that was not there.
// Importing the package makes that impossible: it would not build.

// analyze runs the generator's analysis over the real main.go.
func analyze(t *testing.T) ([]apidocs.Route, apidocs.Meta) {
	t.Helper()
	routes, meta, unknown, err := apidocs.Analyze("main.go")
	if err != nil {
		t.Fatalf("analyze main.go: %v", err)
	}
	if len(unknown) > 0 {
		var b strings.Builder
		b.WriteString("route(s) use an unrecognised middleware chain, so their auth " +
			"requirement cannot be documented:\n")
		for _, r := range unknown {
			fmt.Fprintf(&b, "  main.go:%d  %s %s\n", r.Line, r.Method, r.Path)
		}
		b.WriteString("Add the chain to chainAuth in apidocs/classify.go.")
		t.Fatal(b.String())
	}
	return routes, meta
}

// main.go has two routers, not one.
//
// ServeMux decides which handler serves a path. Before that, the CORS handler
// tests the path against a hardcoded allowlist of prefixes and 404s anything
// outside it. A route can therefore be registered perfectly and still be dead
// in production — no compile error, nothing in a diff to notice, because the
// two live hundreds of lines apart.
//
// This has already happened twice: /api/portal/* when the customer portal
// landed, and /api/customer/*, which shipped dead in PR #140 and stayed that
// way across several releases until it was found while writing the API docs.
func TestEveryRegisteredRouteIsReachable(t *testing.T) {
	routes, _ := analyze(t)

	var dead []string
	for _, r := range routes {
		if r.Unreachable {
			dead = append(dead, fmt.Sprintf("  %s %s  (main.go:%d)", r.Method, r.Path, r.Line))
		}
	}
	if len(dead) == 0 {
		return
	}
	sort.Strings(dead)
	t.Errorf(`%d registered route(s) are unreachable at runtime:

%s

The prefix allowlist in main.go's corsHandler rejects them before ServeMux is
consulted, so they return 404 in production.

Fix: add the missing prefix to that condition in main.go.`,
		len(dead), strings.Join(dead, "\n"))
}

// The generated API docs are the published contract. If someone adds a route
// and does not regenerate, the docs silently understate the API surface — the
// failure mode the generator exists to prevent.
func TestGeneratedAPIDocsAreCurrent(t *testing.T) {
	routes, meta := analyze(t)

	stale, err := apidocs.Stale("docs", routes, meta)
	if err != nil {
		t.Fatalf("compare generated docs: %v", err)
	}
	if len(stale) > 0 {
		t.Errorf(`the generated API docs are out of date: %s

Run this and commit the result:

    go run ./cmd/gen-apidocs`, strings.Join(stale, ", "))
	}
}

// The generated docs are committed and compared against a fresh run, so
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
		routes, meta := analyze(t)
		dir := t.TempDir()
		for _, target := range apidocs.Targets() {
			path := filepath.Join(dir, target.Name)
			if err := target.Emit(path, routes, meta); err != nil {
				t.Fatalf("run %d: emit %s: %v", i, target.Name, err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("run %d: read %s: %v", i, target.Name, err)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(body))
			if i == 0 {
				first[target.Name] = sum
				continue
			}
			if sum != first[target.Name] {
				t.Errorf("%s differs between identical runs (run 0 vs run %d).\n"+
					"Generation must be deterministic: look for map iteration feeding "+
					"output, or an unstable sort with no tiebreak.", target.Name, i)
			}
		}
	}
}

// Every unauthenticated endpoint is a deliberate decision, so the set of them
// is pinned here. Adding one is fine — but it must be a conscious edit to this
// list, reviewed as such, rather than something that arrives with a feature.
//
// A route appearing here that should not be public is a security bug; a public
// route missing from here means someone widened the attack surface silently.
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

	routes, _ := analyze(t)
	got := map[string]bool{}
	for _, r := range apidocs.PublicRoutes(routes) {
		got[r.Method+" "+r.Path] = true
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
