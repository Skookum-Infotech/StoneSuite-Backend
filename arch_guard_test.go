package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDepFreePackages enforces the CLAUDE.md invariant that the query/ and ai/
// packages stay provider-agnostic: neither may import any of this module's
// application packages (see the "Record Filter Engine" and "AI / RAG" rules).
// Only reviewer vigilance guarded this before; here it is mechanical and runs
// under `go test ./...` locally and in CI. Direct imports are sufficient to
// check -- third-party and stdlib packages cannot import back into this module.
func TestDepFreePackages(t *testing.T) {
	const modulePrefix = "stonesuite-backend/"

	// Each dep-free subtree maps to the self-prefix that is legitimately
	// importable from within it (a package importing its own subpackages).
	subtrees := map[string]string{
		"query": "stonesuite-backend/query",
		"ai":    "stonesuite-backend/ai",
	}

	fset := token.NewFileSet()
	for root, self := range subtrees {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				if !strings.HasPrefix(p, modulePrefix) {
					continue // stdlib or third-party — fine
				}
				if p == self || strings.HasPrefix(p, self+"/") {
					continue // importing within the same dep-free subtree — fine
				}
				t.Errorf("%s imports app package %q — %s/ must stay dependency-free of app packages (CLAUDE.md invariant)", path, p, root)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s/: %v", root, err)
		}
	}
}
