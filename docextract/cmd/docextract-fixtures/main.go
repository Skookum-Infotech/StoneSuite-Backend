// Command docextract-fixtures regenerates the synthetic sales-order fixtures
// under testdata/docextract/sales_order (PDF/DOCX plus .expected.json).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	filePerm = 0o644
	dirPerm  = 0o755
)

func main() {
	out := flag.String("out", "testdata/docextract/sales_order", "output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "docextract-fixtures:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	for _, s := range allSpecs() {
		file, data := s.render()
		if err := os.WriteFile(filepath.Join(dir, file), data, filePerm); err != nil {
			return fmt.Errorf("write %s: %w", file, err)
		}
		exp, err := json.MarshalIndent(s.expected(), "", "  ")
		if err != nil {
			return fmt.Errorf("marshal expected for %s: %w", s.name, err)
		}
		base := file[:len(file)-len(filepath.Ext(file))]
		if err := os.WriteFile(filepath.Join(dir, base+".expected.json"), append(exp, '\n'), filePerm); err != nil {
			return fmt.Errorf("write expected for %s: %w", s.name, err)
		}
		fmt.Println("wrote", file)
	}
	return nil
}
