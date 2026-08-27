// Command gen-apidocs generates the API reference, an OpenAPI 3.1 document and
// a Postman collection from the route table in main.go.
//
//	go run ./cmd/gen-apidocs          # rewrite docs/
//	go run ./cmd/gen-apidocs -check   # fail if docs are stale (CI)
//
// The work lives in package apidocs so the guard tests can call it directly
// rather than shelling out to this binary. That is not only faster: a missing
// or broken generator becomes a compile error caught by `go build`, instead of
// a runtime failure discovered by a test — which is how an earlier version of
// this tooling shipped a .gitignore rule that silently excluded the generator's
// own source from the commit.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"stonesuite-backend/apidocs"
)

func main() {
	var (
		source = flag.String("source", "main.go", "Go file containing the route registrations")
		outDir = flag.String("out", "docs", "directory to write generated docs into")
		verify = flag.Bool("check", false,
			"exit non-zero if the generated docs differ from what is on disk (for CI)")
	)
	flag.Parse()

	if err := run(*source, *outDir, *verify); err != nil {
		fmt.Fprintln(os.Stderr, "gen-apidocs:", err)
		os.Exit(1)
	}
}

func run(source, outDir string, verify bool) error {
	routes, meta, unknown, err := apidocs.Analyze(source)
	if err != nil {
		return err
	}
	if len(unknown) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d route(s) use a middleware chain this generator does not recognise.\n", len(unknown))
		b.WriteString("Add the chain to chainAuth in apidocs/classify.go rather than guessing:\n")
		for _, r := range unknown {
			fmt.Fprintf(&b, "  main.go:%d  %s %s\n", r.Line, r.Method, r.Path)
		}
		return fmt.Errorf("%s", b.String())
	}

	if verify {
		stale, err := apidocs.Stale(outDir, routes, meta)
		if err != nil {
			return err
		}
		if len(stale) > 0 {
			return fmt.Errorf("generated docs are stale: %s\nrun: go run ./cmd/gen-apidocs",
				strings.Join(stale, ", "))
		}
		fmt.Println("generated docs are up to date")
		return nil
	}

	for _, t := range apidocs.Targets() {
		path := filepath.Join(outDir, t.Name)
		if err := t.Emit(path, routes, meta); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	fmt.Printf("%d endpoints", len(routes))
	if meta.Unreachable > 0 {
		fmt.Printf("  (%d UNREACHABLE — see the reference)", meta.Unreachable)
	}
	fmt.Println()
	return nil
}
