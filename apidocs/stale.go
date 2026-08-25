package apidocs

import (
	"os"
	"path/filepath"
)

// Stale reports which generated artifacts in dir differ from a fresh
// generation. An empty result means the committed docs are current.
//
// Generation runs into a temporary directory so this never mutates the working
// tree — a check that rewrote the files it was checking would always pass.
func Stale(dir string, routes []Route, meta Meta) ([]string, error) {
	tmp, err := os.MkdirTemp("", "apidocs")
	if err != nil {
		return nil, err
	}
	// Cleanup of a temp dir cannot meaningfully fail the caller — the compare
	// has already happened — but the discard is explicit rather than silent.
	defer func() { _ = os.RemoveAll(tmp) }()

	var stale []string
	for _, t := range Targets() {
		fresh := filepath.Join(tmp, t.Name)
		if err := t.Emit(fresh, routes, meta); err != nil {
			return nil, err
		}
		want, err := os.ReadFile(fresh)
		if err != nil {
			return nil, err
		}
		got, err := os.ReadFile(filepath.Join(dir, t.Name))
		if err != nil {
			stale = append(stale, t.Name+" (missing)")
			continue
		}
		if string(got) != string(want) {
			stale = append(stale, t.Name)
		}
	}
	return stale, nil
}
