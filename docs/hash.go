package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
)

// ContentHash returns a hex-encoded SHA-256 digest over every file embedded
// in FS: its name and bytes, in sorted-name order so the result is stable
// regardless of go:embed directive order. It changes whenever a doc's text
// changes or a doc is added to or removed from the corpus, which is what
// boot-time help-corpus sync compares against to decide whether a re-ingest
// is needed.
func ContentHash() (string, error) {
	var names []string
	if err := fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, path)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk docs FS: %w", err)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		data, err := FS.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		h.Write([]byte(name))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
