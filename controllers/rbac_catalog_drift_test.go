package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// crmActions are the actions every CRM/workflow entity handler enforces. A CRM
// resource missing any of these from the permission catalog is an *ungrantable*
// endpoint: no role can ever be given the capability, so the route is
// permanently locked. This is the drift this test guards against.
var crmActions = []authz.Action{
	authz.ActionCreate,
	authz.ActionRead,
	authz.ActionUpdate,
	authz.ActionDelete,
	authz.ActionTransition,
}

// TestResourceForKeyResourcesAreGrantable asserts that every resource the CRM
// router can authorize against (via resourceForKey) is present in the authz
// catalog with the full CRM action set. The set of workflow keys is discovered
// by parsing resourceForKey's source, so adding a new `case "x":` without adding
// the matching catalog entries fails this test — catalog and enforcement cannot
// silently drift apart.
func TestResourceForKeyResourcesAreGrantable(t *testing.T) {
	keys := caseKeysOfFunc(t, "crm.go", "resourceForKey")
	require.NotEmpty(t, keys, "parsed no case keys from resourceForKey — test/parse is broken")
	// Guard against a silent parse regression returning too few keys: known CRM
	// entities must be discovered.
	require.Subset(t, keys, []string{"lead", "prospect", "customer", "sales_order"},
		"AST parse of resourceForKey missed known keys — parser regression")

	for _, key := range keys {
		res := resourceForKey(key)
		for _, action := range crmActions {
			assert.Truef(t, authz.IsValidPermission(res, action),
				"workflow key %q maps to resource %q but permission {%s, %s} is missing from the authz catalog "+
					"(ungrantable endpoint). Add it to catalog in authz/catalog.go.",
				key, res, res, action)
		}
	}
}

// caseKeysOfFunc parses the given Go source file and returns the string literals
// used as `case "..."` labels inside the named function. It lets the test
// enumerate resourceForKey's keys from source rather than duplicating the switch.
func caseKeysOfFunc(t *testing.T, filename, funcName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	require.NoError(t, err, "parse %s", filename)

	var keys []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range cc.List {
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					keys = append(keys, unquote(t, lit.Value))
				}
			}
			return true
		})
	}
	return keys
}

// unquote strips the surrounding double quotes from a Go string literal token.
func unquote(t *testing.T, lit string) string {
	t.Helper()
	require.GreaterOrEqual(t, len(lit), 2, "malformed string literal %q", lit)
	return lit[1 : len(lit)-1]
}
