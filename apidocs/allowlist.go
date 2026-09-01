package apidocs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// Allowlist is the second, non-obvious router in main.go.
//
// Before the request reaches ServeMux, the CORS handler tests the path against
// a hardcoded set of exact matches and prefixes, and 404s anything outside it.
// A route can therefore be correctly registered on the mux and still be dead —
// which is exactly what happened to /api/portal/* and, at the time of writing,
// to /api/customer/*.
//
// Parsing it lets the generator mark those endpoints rather than documenting
// them as working.
type Allowlist struct {
	Exact    map[string]bool
	Prefixes []string
}

// Permits reports whether a path survives the allowlist.
func (a *Allowlist) Permits(path string) bool {
	if a == nil {
		return true // no allowlist found; assume the guard is gone
	}
	if a.Exact[path] {
		return true
	}
	for _, p := range a.Prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// ParseAllowlist extracts the exact paths and prefixes from the guard condition.
//
// It looks for the `if` whose condition compares path against string literals
// and calls strings.HasPrefix(path, ...). Returns nil when no such condition is
// found, so a refactor that removes the guard degrades to "everything is
// reachable" rather than reporting the whole API as dead.
func ParseAllowlist(filename string) (*Allowlist, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s for allowlist: %w", filename, err)
	}

	var best *Allowlist
	ast.Inspect(file, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Cond == nil {
			return true
		}
		got := collectAllowlist(ifStmt.Cond)
		if got == nil {
			return true
		}
		// Several conditions mention paths; the guard is the one with the most
		// terms, since it enumerates every top-level surface.
		if best == nil || len(got.Exact)+len(got.Prefixes) > len(best.Exact)+len(best.Prefixes) {
			best = got
		}
		return true
	})
	return best, nil
}

// collectAllowlist walks a boolean condition gathering `path != "x"` literals
// and strings.HasPrefix(path, "y") calls.
func collectAllowlist(cond ast.Expr) *Allowlist {
	out := &Allowlist{Exact: map[string]bool{}}

	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch v := e.(type) {
		case *ast.BinaryExpr:
			if v.Op == token.LAND {
				walk(v.X)
				walk(v.Y)
				return
			}
			// path != "literal"
			if v.Op == token.NEQ {
				if isPathIdent(v.X) {
					if s, ok := stringLit(v.Y); ok {
						out.Exact[s] = true
					}
				}
			}
		case *ast.UnaryExpr:
			if v.Op == token.NOT {
				walk(v.X)
			}
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HasPrefix" || len(v.Args) != 2 {
				return
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "strings" || !isPathIdent(v.Args[0]) {
				return
			}
			if s, ok := stringLit(v.Args[1]); ok {
				out.Prefixes = append(out.Prefixes, s)
			}
		}
	}
	walk(cond)

	// Only a condition that names several API surfaces is the guard we want.
	if len(out.Prefixes) < 2 {
		return nil
	}
	return out
}

func isPathIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "path"
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
