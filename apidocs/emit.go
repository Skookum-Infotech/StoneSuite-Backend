package apidocs

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// surfaceOrder controls the order surfaces appear in the generated reference.
// Public-facing first, since that is what a reader most often needs.
var surfaceOrder = []string{"system", "auth", "onboarding", "portal", "customer", "platform", "tenant"}

var surfaceBlurb = map[string]string{
	"system":     "Liveness, readiness and metrics. No credential.",
	"auth":       "Staff sign-in, session rotation, password reset and SAML SSO.",
	"onboarding": "Public tenant onboarding and workspace-user invitations.",
	"portal":     "Customer portal — scoped read access, invitations, workspace switching.",
	"customer":   "Second customer surface from PR #140. See the overlap note in architecture-overview.md.",
	"platform":   "Platform-admin operations across tenants.",
	"tenant":     "The staff application. Every route requires a JWT and resolves a tenant database.",
}

// pathGroup buckets a tenant route by its first meaningful segment, so the
// 390-odd tenant endpoints render as modules rather than one flat list.
var segRe = regexp.MustCompile(`^/api/[a-z-]+/([a-z0-9-]+)`)

func pathGroup(path string) string {
	if m := segRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return "(root)"
}

// Meta carries facts about the generation run into the emitted documents.
type Meta struct {
	Version     string
	Unreachable int
}

// DocsVersion is stamped on the OpenAPI document and Postman collection. Bump
// it by hand when the API changes shape in a way consumers must notice.
//
// Deliberately NOT derived from git: the generated files are committed and CI
// compares them against a fresh run. Anything that varies between the commit
// that generated them and the commit CI runs at — a SHA, a timestamp — would
// fail that comparison on every run and train people to ignore it. Generated
// output must be a pure function of the source it is generated from.
const DocsVersion = "1.0.0"

// Target is one generated artifact.
type Target struct {
	Name string
	Emit func(path string, routes []Route, meta Meta) error
}

// Targets lists every artifact the generator produces.
func Targets() []Target {
	return []Target{
		{"api-reference.md", EmitReference},
		{"openapi.json", EmitOpenAPI},
		{"StoneSuite.postman_collection.json", EmitPostman},
	}
}

// Analyze reads the route table and the prefix allowlist out of source, marks
// routes the allowlist makes unreachable, and reports any route whose
// middleware chain is unrecognised.
//
// Unknown chains are returned rather than guessed at: a wrong "requires no
// credential" claim in published docs is worse than a build failure.
func Analyze(source string) (routes []Route, meta Meta, unknown []Route, err error) {
	routes, err = ParseRoutes(source)
	if err != nil {
		return nil, Meta{}, nil, err
	}
	if len(routes) == 0 {
		return nil, Meta{}, nil, fmt.Errorf(
			"no routes found in %s — the registration shape may have changed", source)
	}
	allow, err := ParseAllowlist(source)
	if err != nil {
		return nil, Meta{}, nil, err
	}
	meta = Meta{Version: DocsVersion}
	for i := range routes {
		if !allow.Permits(routes[i].Path) {
			routes[i].Unreachable = true
			meta.Unreachable++
		}
		if routes[i].Auth == AuthUnknown {
			unknown = append(unknown, routes[i])
		}
	}
	return routes, meta, unknown, nil
}

// PublicRoutes returns every endpoint that needs no credential.
func PublicRoutes(routes []Route) []Route {
	var out []Route
	for _, r := range routes {
		if r.Auth == AuthPublic || r.Auth == AuthPublicLimited {
			out = append(out, r)
		}
	}
	return out
}

// EmitReference writes the human-readable route reference.
func EmitReference(path string, routes []Route, meta Meta) error {
	var b strings.Builder
	b.WriteString("# StoneSuite Backend — API Reference\n\n")
	b.WriteString("> **Generated file — do not edit by hand.**\n> Regenerate with `go run ./cmd/gen-apidocs`.\n")
	b.WriteString("> Narrative and architecture live in [architecture-overview.md](architecture-overview.md).\n\n")
	fmt.Fprintf(&b, "%d endpoints across %d surfaces, read from `main.go`.\n\n", len(routes), len(surfaceOrder))

	// Auth posture summary — the first thing a security reviewer wants.
	b.WriteString("## Auth posture at a glance\n\n")
	b.WriteString("| Requires | Endpoints |\n|---|---:|\n")
	counts := map[string]int{}
	for _, r := range routes {
		counts[r.Auth]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	// Sort by count descending, then by name — the name tiebreak is required,
	// not cosmetic. Go randomises map iteration and sort.Slice is not stable,
	// so two postures with equal counts would otherwise swap between runs and
	// make this committed file differ from a fresh generation ~40% of the time,
	// breaking the CI drift check at random.
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %d |\n", k, counts[k])
	}
	b.WriteString("\n")

	if meta.Unreachable > 0 {
		b.WriteString("> ⚠️ **")
		fmt.Fprintf(&b, "%d endpoint(s) are registered but unreachable", meta.Unreachable)
		b.WriteString("** — the prefix allowlist in `main.go` rejects them before the router runs. Marked 🚫 below.\n\n")
	}

	// Every unauthenticated endpoint, listed explicitly.
	b.WriteString("## Unauthenticated endpoints\n\n")
	b.WriteString("Every endpoint reachable with no credential. Worth re-reading whenever this list grows.\n\n")
	b.WriteString("| Method | Path | Notes |\n|---|---|---|\n")
	for _, r := range routes {
		if r.Auth != AuthPublic && r.Auth != AuthPublicLimited {
			continue
		}
		note := "rate-limited per IP"
		if r.Auth == AuthPublic {
			note = "no rate limit"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", r.Method, r.Path, note)
	}
	b.WriteString("\n")

	// Per-surface tables.
	for _, surface := range surfaceOrder {
		var in []Route
		for _, r := range routes {
			if r.Group == surface {
				in = append(in, r)
			}
		}
		if len(in) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## `%s` — %d endpoints\n\n%s\n\n", surface, len(in), surfaceBlurb[surface])

		groups := map[string][]Route{}
		var order []string
		for _, r := range in {
			g := pathGroup(r.Path)
			if _, seen := groups[g]; !seen {
				order = append(order, g)
			}
			groups[g] = append(groups[g], r)
		}
		sort.Strings(order)

		for _, g := range order {
			if len(order) > 1 {
				fmt.Fprintf(&b, "### %s\n\n", g)
			}
			b.WriteString("| Method | Path | Requires | Handler |\n|---|---|---|---|\n")
			for _, r := range groups[g] {
				flag := ""
				if r.Unreachable {
					flag = "🚫 "
				}
				fmt.Fprintf(&b, "| `%s` | %s`%s` | %s | `%s` |\n",
					r.Method, flag, r.Path, r.Auth, r.Handler)
			}
			b.WriteString("\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// EmitOpenAPI writes an OpenAPI 3.1 document.
func EmitOpenAPI(path string, routes []Route, meta Meta) error {
	paths := map[string]map[string]any{}
	// routes arrive sorted, so operationId assignment is deterministic.
	seenIDs := map[string]bool{}

	for _, r := range routes {
		item, ok := paths[r.Path]
		if !ok {
			item = map[string]any{}
			paths[r.Path] = item
		}
		methods := []string{strings.ToLower(r.Method)}
		if r.Method == "ANY" {
			// ServeMux patterns without a method accept all of them; the
			// handler switches internally. Document the verbs it implements.
			methods = []string{"get", "post"}
		}
		for _, m := range methods {
			op := map[string]any{
				"summary":     r.Handler,
				"operationId": uniqueOperationID(operationID(m, r.Path), seenIDs),
				"tags":        []string{r.Group},
				"responses": map[string]any{
					"200": map[string]any{"description": "Success"},
					"400": map[string]any{"description": "Invalid request"},
					"401": map[string]any{"description": "Authentication required"},
					"403": map[string]any{"description": "Not permitted for this session"},
					"404": map[string]any{"description": "Not found, or outside the caller's scope"},
				},
			}
			if r.Auth != AuthPublic && r.Auth != AuthPublicLimited {
				op["security"] = []map[string][]string{{"bearerAuth": {}}}
			}
			if params := pathParams(r.Path); len(params) > 0 {
				op["parameters"] = params
			}
			if r.Unreachable {
				op["deprecated"] = true
				op["description"] = "UNREACHABLE: rejected by the prefix allowlist in main.go before routing."
			}
			item[m] = op
		}
	}

	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "StoneSuite Backend API",
			"version": meta.Version,
			"description": "Generated from main.go by cmd/gen-apidocs. " +
				"Request and response schemas are not modelled: this document is a complete, " +
				"trustworthy index of endpoints and their auth requirements, not a payload contract.",
		},
		"servers": []map[string]any{
			{"url": "http://localhost:8080", "description": "Local"},
		},
		"tags":  openAPITags(),
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{
					"type": "http", "scheme": "bearer", "bearerFormat": "JWT",
					"description": "Staff tokens carry no `kind` claim; customer-portal tokens " +
						"carry `kind=portal` and are only valid under /api/portal/.",
				},
			},
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal openapi: %w", err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func openAPITags() []map[string]any {
	tags := make([]map[string]any, 0, len(surfaceOrder))
	for _, s := range surfaceOrder {
		tags = append(tags, map[string]any{"name": s, "description": surfaceBlurb[s]})
	}
	return tags
}

var paramRe = regexp.MustCompile(`\{([a-zA-Z_]\w*)\}`)

func pathParams(path string) []map[string]any {
	var out []map[string]any
	for _, m := range paramRe.FindAllStringSubmatch(path, -1) {
		out = append(out, map[string]any{
			"name": m[1], "in": "path", "required": true,
			"schema": map[string]any{"type": "string"},
		})
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// operationID builds a stable, unique identifier for an operation.
//
// operationId must be unique across an OpenAPI document — code generators use
// it as a function name, and duplicates make them fail or silently drop
// operations. Two things here would otherwise collide:
//
//   - "/api/tenant/roles" and "/api/tenant/roles/" slugify identically, even
//     though in ServeMux the trailing slash means a SUBTREE match and is a
//     genuinely different route. That distinction is preserved as a suffix
//     rather than erased.
//   - Anything still colliding gets a numeric suffix from the caller's
//     uniqueness pass, so the document is valid even if a new collision shape
//     appears later.
func operationID(method, path string) string {
	trimmed := strings.TrimPrefix(path, "/api/")
	subtree := strings.HasSuffix(trimmed, "/")
	s := strings.Trim(nonAlnum.ReplaceAllString(trimmed, "_"), "_")
	id := method + "_" + s
	if subtree {
		id += "_subtree"
	}
	return id
}

// uniqueOperationID returns id, or id with the smallest numeric suffix that has
// not been used yet. Mutates seen.
func uniqueOperationID(id string, seen map[string]bool) string {
	if !seen[id] {
		seen[id] = true
		return id
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s_%d", id, n)
		if !seen[candidate] {
			seen[candidate] = true
			return candidate
		}
	}
}

// EmitPostman writes a Postman v2.1 collection, foldered by surface.
func EmitPostman(path string, routes []Route, meta Meta) error {
	folders := map[string]*pmFolder{}
	var order []string

	for _, r := range routes {
		f, ok := folders[r.Group]
		if !ok {
			f = &pmFolder{Name: r.Group, Description: surfaceBlurb[r.Group]}
			folders[r.Group] = f
			order = append(order, r.Group)
		}
		method := r.Method
		if method == "ANY" {
			method = "GET"
		}
		desc := fmt.Sprintf("Requires: %s\nRegistered at main.go:%d", r.Auth, r.Line)
		if r.Unreachable {
			desc = "⚠️ UNREACHABLE — rejected by the prefix allowlist in main.go.\n\n" + desc
		}
		item := pmItem{
			Name: fmt.Sprintf("%s %s", method, r.Path),
			Request: pmRequest{
				Method:      method,
				Description: desc,
				URL: pmURL{
					Raw:  "{{baseUrl}}" + r.Path,
					Host: []string{"{{baseUrl}}"},
					Path: strings.Split(strings.TrimPrefix(r.Path, "/"), "/"),
				},
			},
		}
		if r.Auth != AuthPublic && r.Auth != AuthPublicLimited {
			tokenVar := "{{staffToken}}"
			if strings.Contains(r.Auth, "portal") {
				tokenVar = "{{portalToken}}"
			} else if strings.Contains(r.Auth, "customer") {
				tokenVar = "{{customerToken}}"
			}
			item.Request.Header = []pmHeader{{Key: "Authorization", Value: "Bearer " + tokenVar}}
		}
		if method == "POST" || method == "PATCH" || method == "PUT" {
			item.Request.Header = append(item.Request.Header,
				pmHeader{Key: "Content-Type", Value: "application/json"})
			item.Request.Body = &pmBody{Mode: "raw", Raw: "{}"}
		}
		f.Item = append(f.Item, item)
	}

	sort.Slice(order, func(i, j int) bool { return surfaceRank(order[i]) < surfaceRank(order[j]) })
	items := make([]*pmFolder, 0, len(order))
	for _, name := range order {
		items = append(items, folders[name])
	}

	coll := pmCollection{
		Info: pmInfo{
			Name: "StoneSuite Backend — " + meta.Version,
			Description: "Generated from main.go by cmd/gen-apidocs.\n\n" +
				"Set `baseUrl`, then `staffToken` / `portalToken` / `customerToken` " +
				"from the relevant login response.",
			Schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
		Item: items,
		Variable: []pmVar{
			{Key: "baseUrl", Value: "http://localhost:8080"},
			{Key: "staffToken", Value: ""},
			{Key: "portalToken", Value: ""},
			{Key: "customerToken", Value: ""},
		},
	}
	out, err := json.MarshalIndent(coll, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal postman: %w", err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func surfaceRank(s string) int {
	for i, v := range surfaceOrder {
		if v == s {
			return i
		}
	}
	return len(surfaceOrder)
}

// ---- Postman collection shapes ----------------------------------------------

type pmCollection struct {
	Info     pmInfo      `json:"info"`
	Item     []*pmFolder `json:"item"`
	Variable []pmVar     `json:"variable"`
}
type pmInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      string `json:"schema"`
}
type pmFolder struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Item        []pmItem `json:"item"`
}
type pmItem struct {
	Name    string    `json:"name"`
	Request pmRequest `json:"request"`
}
type pmRequest struct {
	Method      string     `json:"method"`
	Header      []pmHeader `json:"header,omitempty"`
	Body        *pmBody    `json:"body,omitempty"`
	URL         pmURL      `json:"url"`
	Description string     `json:"description,omitempty"`
}
type pmHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type pmBody struct {
	Mode string `json:"mode"`
	Raw  string `json:"raw"`
}
type pmURL struct {
	Raw  string   `json:"raw"`
	Host []string `json:"host"`
	Path []string `json:"path"`
}
type pmVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
