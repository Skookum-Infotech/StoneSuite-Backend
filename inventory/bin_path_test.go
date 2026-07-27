package inventory

import "testing"

func TestBuildPath(t *testing.T) {
	tests := []struct {
		name       string
		parentPath string
		code       string
		wantPath   string
		wantDepth  int
		wantErr    bool
		errSubstr  string
	}{
		{"top-level bin", "", "YARD-A", "YARD-A", 0, false, ""},
		{"one level down", "YARD-A", "AF-03", "YARD-A/AF-03", 1, false, ""},
		{"three levels down", "YARD-A/AF-03", "SLOT-7", "YARD-A/AF-03/SLOT-7", 2, false, ""},
		{
			name:       "the deepest allowed nesting",
			parentPath: "A/B/C/D", code: "E",
			wantPath: "A/B/C/D/E", wantDepth: 4,
		},
		{
			name:       "one level past the cap is rejected",
			parentPath: "A/B/C/D/E", code: "F",
			wantErr: true, errSubstr: "nest more than",
		},
		{
			// A code containing the separator makes the path impossible to
			// split back into segments.
			name:       "a code containing the separator is rejected",
			parentPath: "YARD-A", code: "AF/03",
			wantErr: true, errSubstr: "cannot contain",
		},
		{"an empty code is rejected", "YARD-A", "", "", 0, true, "required"},
		{"a whitespace-only code is rejected", "YARD-A", "   ", "", 0, true, "required"},
		{"code is trimmed", "YARD-A", "  AF-03  ", "YARD-A/AF-03", 1, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, depth, err := BuildPath(tt.parentPath, tt.code)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("BuildPath() = %q, want error", path)
				}
				if !contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
			if depth != tt.wantDepth {
				t.Errorf("depth = %d, want %d", depth, tt.wantDepth)
			}
			// Depth and path must never disagree, or chk_bin_depth rejects the row.
			if depth != DepthOf(path) {
				t.Errorf("depth %d disagrees with DepthOf(%q) = %d", depth, path, DepthOf(path))
			}
		})
	}
}

func TestWouldCycle(t *testing.T) {
	tests := []struct {
		name          string
		childPath     string
		newParentPath string
		want          bool
	}{
		{"a normal reparent is fine", "YARD-A/AF-03", "YARD-B", false},
		{"a bin cannot be its own parent", "YARD-A", "YARD-A", true},
		{
			// The multi-step case a CHECK constraint cannot express, because it
			// cannot run a subquery.
			name:      "a bin cannot move under its own child",
			childPath: "YARD-A", newParentPath: "YARD-A/AF-03", want: true,
		},
		{
			name:      "a bin cannot move under its own grandchild",
			childPath: "YARD-A", newParentPath: "YARD-A/AF-03/SLOT-7", want: true,
		},
		{
			// The prefix trap: 'YARD-A1' merely starts with 'YARD-A', it is not
			// a descendant of it. Comparing on the raw prefix would wrongly
			// refuse this legitimate move.
			name:      "a sibling with a shared code prefix is not a descendant",
			childPath: "YARD-A", newParentPath: "YARD-A1", want: false,
		},
		{
			name:      "a deeper sibling with a shared prefix is not a descendant",
			childPath: "YARD-A", newParentPath: "YARD-A1/AF-01", want: false,
		},
		{"an empty child path cannot cycle", "", "YARD-A", false},
		{"moving to top level cannot cycle", "YARD-A/AF-03", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WouldCycle(tt.childPath, tt.newParentPath); got != tt.want {
				t.Errorf("WouldCycle(%q, %q) = %v, want %v",
					tt.childPath, tt.newParentPath, got, tt.want)
			}
		})
	}
}

func TestRepathSubtree(t *testing.T) {
	tests := []struct {
		name        string
		oldAncestor string
		newAncestor string
		descendant  string
		wantPath    string
		wantDepth   int
		wantErr     bool
	}{
		{
			name:        "renaming a parent rewrites its child",
			oldAncestor: "YARD-A", newAncestor: "YARD-NORTH",
			descendant: "YARD-A/AF-03",
			wantPath:   "YARD-NORTH/AF-03", wantDepth: 1,
		},
		{
			name:        "renaming a parent rewrites its grandchild",
			oldAncestor: "YARD-A", newAncestor: "YARD-NORTH",
			descendant: "YARD-A/AF-03/SLOT-7",
			wantPath:   "YARD-NORTH/AF-03/SLOT-7", wantDepth: 2,
		},
		{
			name:        "reparenting deepens the subtree",
			oldAncestor: "AF-03", newAncestor: "YARD-A/ROW-1/AF-03",
			descendant: "AF-03/SLOT-7",
			wantPath:   "YARD-A/ROW-1/AF-03/SLOT-7", wantDepth: 3,
		},
		{
			// A reparent that pushes descendants past the cap has to fail, or
			// chk_bin_depth rejects the row mid-transaction.
			name:        "a reparent that would exceed the depth cap is rejected",
			oldAncestor: "AF-03", newAncestor: "A/B/C/AF-03",
			descendant: "AF-03/SLOT-7/SUB",
			wantErr:    true,
		},
		{
			name:        "a path outside the subtree is a programming error",
			oldAncestor: "YARD-A", newAncestor: "YARD-NORTH",
			descendant: "YARD-B/AF-01",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, depth, err := RepathSubtree(tt.oldAncestor, tt.newAncestor, tt.descendant)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RepathSubtree() = %q, want error", path)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
			if depth != tt.wantDepth {
				t.Errorf("depth = %d, want %d", depth, tt.wantDepth)
			}
		})
	}
}

func TestSubtreePrefixExcludesTheBinItself(t *testing.T) {
	// The prefix must match strict descendants only. Including the bin itself
	// would make a "delete me and my children" check always report children.
	got := SubtreePrefix("YARD-A")
	if got != "YARD-A/%" {
		t.Fatalf("SubtreePrefix() = %q, want %q", got, "YARD-A/%")
	}
}

func TestValidateBinType(t *testing.T) {
	for _, ok := range []string{"yard", "rack", "aframe", "aisle", "shelf", "floor", "staging"} {
		if err := ValidateBinType(ok); err != nil {
			t.Errorf("ValidateBinType(%q) rejected a type the schema accepts: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "YARD", "bin", "pallet"} {
		if err := ValidateBinType(bad); err == nil {
			t.Errorf("ValidateBinType(%q) accepted a type chk_bin_type would reject", bad)
		}
	}
}
