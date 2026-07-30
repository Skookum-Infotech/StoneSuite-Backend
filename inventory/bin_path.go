package inventory

// bin_path.go — bin tree arithmetic. Pure: no database, no context.
//
// inventory_bin is a flat table with an optional self-parent (spec AD-1), so
// "where is this slab?" would need a WITH RECURSIVE on every read. Instead the
// ancestor path is materialised into bin_path ('YARD-A/AF-03/SLOT-7') and the
// rules for building and validating it live here, where they can be tested
// without a database.

import (
	"fmt"
	"strings"
)

// MaxBinDepth mirrors chk_bin_depth in the schema. Depth 0 is a top-level bin,
// so a path may hold at most MaxBinDepth+1 segments.
const MaxBinDepth = 4

// binPathSep separates path segments. Bin codes are validated to exclude it,
// so a path can always be split back into its segments unambiguously.
const binPathSep = "/"

// Bin types, mirroring chk_bin_type.
var validBinTypes = map[string]bool{
	"yard": true, "rack": true, "aframe": true, "aisle": true,
	"shelf": true, "floor": true, "staging": true,
}

// ValidateBinType reports whether a bin type is one the schema accepts.
func ValidateBinType(t string) error {
	if !validBinTypes[t] {
		return ClientError{Msg: fmt.Sprintf("Unknown bin type %q.", t)}
	}
	return nil
}

// ValidateBinCode rejects codes that would corrupt a materialised path.
// A code containing the separator would make the path ambiguous to split, and
// an empty code would produce a '//' run that no longer round-trips.
func ValidateBinCode(code string) error {
	c := strings.TrimSpace(code)
	if c == "" {
		return ClientError{Msg: "Bin code is required."}
	}
	if strings.Contains(c, binPathSep) {
		return ClientError{Msg: "Bin code cannot contain '" + binPathSep + "'."}
	}
	if len(c) > 30 {
		return ClientError{Msg: "Bin code cannot be longer than 30 characters."}
	}
	return nil
}

// BuildPath returns the materialised path and depth for a bin with the given
// code under the given parent path. An empty parentPath means a top-level bin.
func BuildPath(parentPath, code string) (string, int, error) {
	if err := ValidateBinCode(code); err != nil {
		return "", 0, err
	}
	code = strings.TrimSpace(code)
	if parentPath == "" {
		return code, 0, nil
	}
	path := parentPath + binPathSep + code
	depth := strings.Count(path, binPathSep)
	if depth > MaxBinDepth {
		return "", 0, ClientError{Msg: fmt.Sprintf(
			"Bins cannot nest more than %d levels deep.", MaxBinDepth+1)}
	}
	return path, depth, nil
}

// DepthOf returns the depth implied by a materialised path.
func DepthOf(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, binPathSep)
}

// WouldCycle reports whether re-parenting the bin at childPath under
// newParentPath would create a cycle.
//
// A bin cannot become a descendant of itself. The database can only express
// the one-step case (chk_bin_not_self), because a CHECK cannot run a subquery —
// so the multi-step case has to be caught here, before the write.
func WouldCycle(childPath, newParentPath string) bool {
	if childPath == "" || newParentPath == "" {
		return false
	}
	if childPath == newParentPath {
		return true
	}
	// The new parent being inside the child's own subtree is exactly the cycle.
	// Comparing against childPath+"/" rather than childPath alone stops
	// 'YARD-A1' from being mistaken for a descendant of 'YARD-A'.
	return strings.HasPrefix(newParentPath, childPath+binPathSep)
}

// RepathSubtree rewrites a descendant's path when its ancestor moves or is
// renamed, returning the new path and depth.
//
// Callers must apply this to the whole subtree in the same transaction as the
// parent's own change. Skipping it leaves stale paths that the
// varchar_pattern_ops prefix index will happily keep returning — the bin looks
// like it is still in its old location, and nothing errors.
func RepathSubtree(oldAncestorPath, newAncestorPath, descendantPath string) (string, int, error) {
	if !strings.HasPrefix(descendantPath, oldAncestorPath) {
		return "", 0, fmt.Errorf("path %q is not under %q", descendantPath, oldAncestorPath)
	}
	suffix := strings.TrimPrefix(descendantPath, oldAncestorPath)
	newPath := newAncestorPath + suffix
	depth := DepthOf(newPath)
	if depth > MaxBinDepth {
		return "", 0, ClientError{Msg: fmt.Sprintf(
			"That move would nest bins more than %d levels deep.", MaxBinDepth+1)}
	}
	return newPath, depth, nil
}

// SubtreePrefix returns the LIKE pattern matching every strict descendant of a
// bin. Used with the varchar_pattern_ops index on bin_path.
func SubtreePrefix(path string) string { return path + binPathSep + "%" }
