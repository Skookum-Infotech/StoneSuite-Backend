package docextract

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These cases are unambiguous under any sane tolerance rule.
func TestReconcile_Unambiguous(t *testing.T) {
	tests := []struct {
		name     string
		lines    []Cents
		subtotal Cents
		wantPass bool
	}{
		{name: "exact match passes", lines: []Cents{10000, 20000, 30000}, subtotal: 60000, wantPass: true},
		{name: "ten dollars off on three lines fails", lines: []Cents{10000, 20000, 30000}, subtotal: 61000, wantPass: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks, panicked := safeReconcile(tt.lines, tt.subtotal)
			if panicked != "" {
				t.Fatalf("Reconcile panicked (withinTolerance not implemented yet): %v", panicked)
			}
			if assert.Len(t, checks, 1) {
				assert.Equal(t, CheckLinesVsSubtotal, checks[0].Name)
				assert.Equal(t, tt.wantPass, checks[0].Passed)
			}
		})
	}
}

// safeReconcile converts the placeholder's panic into a reportable failure so
// it cannot abort the rest of the package's tests.
func safeReconcile(lines []Cents, subtotal Cents) (checks []CheckResult, panicked string) {
	defer func() {
		if r := recover(); r != nil {
			panicked = fmt.Sprint(r)
		}
	}()
	checks = Reconcile(lines, subtotal, 0, 0, 0, 0)
	return checks, ""
}

func TestWithinTolerance(t *testing.T) {
	tests := []struct {
		name             string
		expected, actual Cents
		lineCount        int
		want             bool
	}{
		{"exact", 1000, 1000, 1, true},
		{"floor covers 2 cents on one line", 1000, 1002, 1, true},
		{"3 cents off on one line fails", 1000, 1003, 1, false},
		{"negative diff symmetric", 1002, 1000, 1, true},
		{"10 lines allow 5 cents", 100000, 100005, 10, true},
		{"10 lines reject 6 cents", 100000, 100006, 10, false},
		{"odd count rounds up", 100000, 100002, 3, true},
		{"large order, one misread digit fails", 1234500, 1243500, 40, false},
		{"zero lines uses floor", 0, 2, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, withinTolerance(tt.expected, tt.actual, tt.lineCount))
		})
	}
}
