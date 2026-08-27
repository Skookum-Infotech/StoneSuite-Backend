package apidocs

import "strings"

// Auth postures, ordered roughly from least to most protected. These strings
// appear verbatim in the generated docs, so a reader can tell at a glance what
// an endpoint requires.
const (
	AuthPublic        = "none"                 // no credential at all
	AuthPublicLimited = "none (rate-limited)"  // no credential, per-IP budget
	AuthStaff         = "staff token"          // JWT, no tenant resolution
	AuthStaffTenant   = "staff token + tenant" // JWT + tenancy resolver
	AuthPortal        = "portal token"         // customer JWT (kind=portal)
	AuthPortalTenant  = "portal token + tenant"
	AuthCustomer      = "customer token"   // customer JWT (principal_type=customer)
	AuthUnknown       = "UNKNOWN — review" // chain not recognised
)

// chainAuth maps a middleware chain name to the credential it demands.
//
// Read directly off main.go's chain definitions. Order matters below: the most
// specific name must be tested first, since portalChain contains "portal" and
// tenantChain contains "tenant".
var chainAuth = []struct {
	marker string
	auth   string
}{
	{"portalPublic", AuthPublicLimited},
	{"portalCrossTenant", AuthPortal},
	{"portalChain", AuthPortalTenant},
	{"customerChain", AuthCustomer},
	{"customerAuthRateLimiter", AuthPublicLimited},
	{"tenantChain", AuthStaffTenant},
	{"aiChain", AuthStaffTenant},
}

// classifyAuth reports what an endpoint requires, and which surface it belongs
// to, from the middleware chain wrapping its handler.
//
// POLICY: an unrecognised chain is reported as AuthUnknown, not as "public"
// and not as "authenticated". This is deliberate and is the one judgement call
// in this generator worth arguing about:
//
//   - Defaulting to "public" would publish a claim that an endpoint needs no
//     credential. If wrong, the docs invite people to probe it.
//   - Defaulting to "authenticated" would hide a genuinely open endpoint from
//     the security review the docs are supposed to enable.
//
// Reporting UNKNOWN refuses to guess and puts the route in front of a human.
// The generator counts these, and the route-coverage test fails the build if
// any appear — so a new chain added to main.go forces an update here rather
// than silently producing a wrong security claim.
func classifyAuth(wrapper, path string) (auth, group string) {
	group = surfaceOf(path)

	for _, c := range chainAuth {
		if strings.Contains(wrapper, c.marker) {
			return c.auth, group
		}
	}

	// Hand-rolled chains that do not go through a named helper.
	hasAuth := strings.Contains(wrapper, "RequireAuth")
	hasTenant := strings.Contains(wrapper, "resolver.Middleware")
	hasPortal := strings.Contains(wrapper, "RequirePortal")
	hasCustomer := strings.Contains(wrapper, "RequireCustomerAuth")

	switch {
	case hasCustomer:
		return AuthCustomer, group
	case hasPortal && hasTenant:
		return AuthPortalTenant, group
	case hasPortal:
		return AuthPortal, group
	case hasAuth && hasTenant:
		return AuthStaffTenant, group
	case hasAuth:
		return AuthStaff, group
	}

	// No auth middleware present. Rate limiting is the only remaining
	// distinction worth surfacing — an unauthenticated endpoint with a per-IP
	// budget is a different risk from one without.
	if strings.Contains(wrapper, "PerIP") {
		return AuthPublicLimited, group
	}
	// A bare handler with no wrapper at all is genuinely public; anything else
	// is a chain shape this generator has not seen.
	if isBareHandler(wrapper) {
		return AuthPublic, group
	}
	return AuthUnknown, group
}

// isBareHandler reports whether the second argument to mux.Handle is a plain
// handler rather than a chain — e.g. http.HandlerFunc(x) or just a func value.
func isBareHandler(wrapper string) bool {
	trimmed := strings.TrimSpace(wrapper)
	return strings.HasPrefix(trimmed, "http.HandlerFunc(") ||
		strings.HasPrefix(trimmed, "func(") ||
		!strings.Contains(trimmed, "(")
}

// surfaceOf buckets a path by its top-level API surface.
func surfaceOf(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/tenant/"):
		return "tenant"
	case strings.HasPrefix(path, "/api/portal/"):
		return "portal"
	case strings.HasPrefix(path, "/api/customer/"):
		return "customer"
	case strings.HasPrefix(path, "/api/platform/"):
		return "platform"
	case strings.HasPrefix(path, "/api/auth/"):
		return "auth"
	case strings.HasPrefix(path, "/api/onboarding"):
		return "onboarding"
	default:
		return "system"
	}
}
