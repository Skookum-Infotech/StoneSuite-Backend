package apidocs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Route is one registered HTTP endpoint, as read out of main.go.
type Route struct {
	Method  string // GET, POST, ... or ANY when the pattern carries no method
	Path    string // e.g. /api/tenant/invoices/{uuid}
	Auth    string // auth posture — see classifyAuth
	Group   string // top-level surface: tenant, portal, customer, auth, platform, ...
	Handler string // e.g. invOps.Get
	Line    int    // line in main.go, so a reader can jump to the registration

	// Unreachable marks a route that is registered on the router but rejected
	// by the prefix allowlist in main.go before routing happens. Such an
	// endpoint returns 404 in production despite existing in the code.
	Unreachable bool
}

// muxMethods are the HTTP methods net/http's ServeMux accepts as a pattern prefix.
var muxMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"HEAD": true, "OPTIONS": true,
}

// ParseRoutes reads every mux.Handle/mux.HandleFunc registration out of a Go
// source file.
//
// Uses go/ast rather than a regex on purpose: a regex silently drops call
// shapes it does not anticipate, and a docs generator that quietly omits
// endpoints is worse than none at all. The AST sees every call.
func ParseRoutes(filename string) ([]Route, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	var routes []Route
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "mux" {
			return true
		}
		if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}

		patterns := patternLiterals(call.Args[0])
		if len(patterns) == 0 {
			return true
		}
		wrapper := exprString(fset, call.Args[1])
		line := fset.Position(call.Pos()).Line

		for _, pattern := range patterns {
			method, path := splitPattern(pattern)
			auth, group := classifyAuth(wrapper, path)
			routes = append(routes, Route{
				Method:  method,
				Path:    path,
				Auth:    auth,
				Group:   group,
				Handler: handlerName(wrapper),
				Line:    line,
			})
		}
		return true
	})

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

// patternLiterals returns the route pattern(s) an argument expression yields.
//
// Almost every registration is a plain string literal. The exception is the
// portal message loop, which builds "/api/portal/" + slug + "/{uuid}/messages"
// from a range over a map — a concatenation whose value is not knowable
// statically. Those are expanded from knownSlugExpansions rather than guessed,
// so the generator never invents an endpoint that does not exist.
func patternLiterals(arg ast.Expr) []string {
	if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil
		}
		return []string{s}
	}
	if bin, ok := arg.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
		return expandConcat(bin)
	}
	return nil
}

// documentSlugs are the URL segments the per-module loops in main.go range
// over. Kept here so a concatenated pattern expands to real endpoints.
var documentSlugs = []string{"sales-orders", "invoices", "payments", "refunds"}

// expandConcat turns `"prefix" + <ident> + "suffix"` into one pattern per
// known slug. Returns nil when the shape is anything else, so an unrecognised
// concatenation is reported as a gap rather than silently dropped.
func expandConcat(bin *ast.BinaryExpr) []string {
	var prefix, suffix string
	var sawIdent bool

	var walk func(ast.Expr) bool
	walk = func(e ast.Expr) bool {
		switch v := e.(type) {
		case *ast.BinaryExpr:
			if v.Op != token.ADD {
				return false
			}
			return walk(v.X) && walk(v.Y)
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return false
			}
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				return false
			}
			if sawIdent {
				suffix += s
			} else {
				prefix += s
			}
			return true
		case *ast.Ident:
			sawIdent = true
			return true
		default:
			return false
		}
	}
	if !walk(bin) || !sawIdent {
		return nil
	}
	out := make([]string, 0, len(documentSlugs))
	for _, slug := range documentSlugs {
		out = append(out, prefix+slug+suffix)
	}
	return out
}

// splitPattern separates the optional method prefix from the path.
func splitPattern(pattern string) (method, path string) {
	fields := strings.Fields(pattern)
	if len(fields) == 2 && muxMethods[fields[0]] {
		return fields[0], fields[1]
	}
	return "ANY", strings.TrimSpace(pattern)
}

// exprString renders an expression back to source, for classification.
func exprString(fset *token.FileSet, e ast.Expr) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, fset, e); err != nil {
		return ""
	}
	return sb.String()
}

var handlerRe = regexp.MustCompile(`\b([a-zA-Z_]\w*)\.([A-Z]\w*)\b`)

// infrastructure names that appear in a chain expression but are not the
// endpoint's handler.
var notHandlers = map[string]bool{
	"middleware": true, "http": true, "resolver": true,
	"tenantRateLimiter": true, "authRateLimiter": true, "aiRateLimiter": true,
	"customerAuthRateLimiter": true, "portalRateLimiter": true,
}

// handlerName picks the Ops.Method reference out of a chain expression.
func handlerName(wrapper string) string {
	matches := handlerRe.FindAllStringSubmatch(wrapper, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		if !notHandlers[matches[i][1]] {
			return matches[i][1] + "." + matches[i][2]
		}
	}
	return ""
}
