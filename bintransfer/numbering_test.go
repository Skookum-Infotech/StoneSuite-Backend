package bintransfer

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		id   int64
		want string
	}{
		{1, "BTRF-000001"},
		{12, "BTRF-000012"},
		{999999, "BTRF-999999"},
		{1000000, "BTRF-1000000"}, // grows past the pad, never truncates
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.id); got != tt.want {
			t.Fatalf("FormatNumber(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

// The Record Numbering config is keyed by workflow key: a mismatch silently
// ignores the tenant's configured format.
func TestNumberTargetWorkflowKey(t *testing.T) {
	if numberTarget.WorkflowKey != "bin_transfer" {
		t.Errorf("numberTarget.WorkflowKey = %q, want bin_transfer", numberTarget.WorkflowKey)
	}
	if numberTarget.UpdateSQL == "" {
		t.Error("numberTarget.UpdateSQL must not be empty")
	}
}
