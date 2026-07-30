package inventory

import "testing"

func ptr(s string) *string { return &s }

// bin is a terse constructor for tree fixtures.
func bin(id, path string, parent *string) Bin {
	return Bin{ID: id, Path: path, ParentID: parent, Depth: DepthOf(path)}
}

func TestAssembleBinTree(t *testing.T) {
	t.Run("nests a three-level yard without losing grandchildren", func(t *testing.T) {
		// The regression this test exists for: assembling forwards copies a
		// child into its parent BEFORE that child's own children are attached,
		// so SLOT-7 silently disappears and the yard appears to have no slots.
		flat := []Bin{
			bin("yard", "YARD-A", nil),
			bin("af", "YARD-A/AF-03", ptr("yard")),
			bin("slot", "YARD-A/AF-03/SLOT-7", ptr("af")),
		}
		roots := assembleBinTree(flat)

		if len(roots) != 1 {
			t.Fatalf("got %d roots, want 1", len(roots))
		}
		if len(roots[0].Children) != 1 {
			t.Fatalf("yard has %d children, want 1", len(roots[0].Children))
		}
		af := roots[0].Children[0]
		if af.ID != "af" {
			t.Fatalf("child = %q, want %q", af.ID, "af")
		}
		if len(af.Children) != 1 {
			t.Fatalf("grandchild lost: A-frame has %d children, want 1", len(af.Children))
		}
		if af.Children[0].ID != "slot" {
			t.Errorf("grandchild = %q, want %q", af.Children[0].ID, "slot")
		}
	})

	t.Run("keeps siblings in path order", func(t *testing.T) {
		// Assembly walks backwards, so appending rather than prepending would
		// hand the UI its bins in reverse.
		flat := []Bin{
			bin("yard", "YARD-A", nil),
			bin("a", "YARD-A/AF-01", ptr("yard")),
			bin("b", "YARD-A/AF-02", ptr("yard")),
			bin("c", "YARD-A/AF-03", ptr("yard")),
		}
		roots := assembleBinTree(flat)
		got := []string{}
		for _, c := range roots[0].Children {
			got = append(got, c.ID)
		}
		want := []string{"a", "b", "c"}
		if len(got) != len(want) {
			t.Fatalf("got %d children, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("children order = %v, want %v", got, want)
			}
		}
	})

	t.Run("surfaces an orphan as a root rather than dropping it", func(t *testing.T) {
		// A bin whose parent was filtered out (inactive, or a different
		// warehouse) must still appear: silently hiding a bin that holds slabs
		// is worse than showing it at the top level.
		flat := []Bin{
			bin("orphan", "YARD-GONE/AF-09", ptr("missing-parent")),
		}
		roots := assembleBinTree(flat)
		if len(roots) != 1 || roots[0].ID != "orphan" {
			t.Fatalf("orphan was dropped; roots = %+v", roots)
		}
	})

	t.Run("handles several independent roots", func(t *testing.T) {
		flat := []Bin{
			bin("y1", "YARD-A", nil),
			bin("y1c", "YARD-A/AF-01", ptr("y1")),
			bin("y2", "YARD-B", nil),
		}
		roots := assembleBinTree(flat)
		if len(roots) != 2 {
			t.Fatalf("got %d roots, want 2", len(roots))
		}
		if len(roots[0].Children) != 1 || len(roots[1].Children) != 0 {
			t.Errorf("children mis-assigned: %d and %d", len(roots[0].Children), len(roots[1].Children))
		}
	})

	t.Run("an empty list yields an empty, non-nil slice", func(t *testing.T) {
		// The handler marshals this straight to JSON; a nil slice would emit
		// null instead of [] and break a client that iterates it.
		roots := assembleBinTree(nil)
		if roots == nil {
			t.Fatal("assembleBinTree(nil) returned nil, want an empty slice")
		}
		if len(roots) != 0 {
			t.Fatalf("got %d roots, want 0", len(roots))
		}
	})

	t.Run("nests the deepest allowed tree", func(t *testing.T) {
		flat := []Bin{
			bin("l0", "A", nil),
			bin("l1", "A/B", ptr("l0")),
			bin("l2", "A/B/C", ptr("l1")),
			bin("l3", "A/B/C/D", ptr("l2")),
			bin("l4", "A/B/C/D/E", ptr("l3")),
		}
		roots := assembleBinTree(flat)
		n, depth := roots[0], 0
		for len(n.Children) > 0 {
			n = n.Children[0]
			depth++
		}
		if depth != MaxBinDepth {
			t.Fatalf("deepest nesting reached %d, want %d", depth, MaxBinDepth)
		}
		if n.ID != "l4" {
			t.Errorf("deepest node = %q, want %q", n.ID, "l4")
		}
	})
}
