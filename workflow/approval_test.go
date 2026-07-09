package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The approval gate's correctness rests on three pure functions:
// approvalStatusOf (none/pending/approved), approvalGateLocked (whether a
// record may leave its state), and buildApproval (the overlay + canApprove).
// They are exercised exhaustively here without a database.

func TestApprovalStatusOf(t *testing.T) {
	tests := []struct {
		name     string
		required int
		approved int
		want     string
	}{
		{"no approvers configured", 0, 0, "none"},
		{"approvers but none signed", 2, 0, "pending"},
		{"partial sign-off", 2, 1, "pending"},
		{"single approver signed", 1, 1, "approved"},
		{"all signed", 3, 3, "approved"},
		{"over-signed is still approved", 2, 3, "approved"},
		{"zero required ignores stray approvals", 0, 2, "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, approvalStatusOf(tc.required, tc.approved))
		})
	}
}

func TestApprovalGateLocked(t *testing.T) {
	tests := []struct {
		name     string
		required int
		approved int
		want     bool
	}{
		{"ungated state is never locked", 0, 0, false},
		{"gated, nobody signed -> locked", 2, 0, true},
		{"gated, partial -> locked", 3, 2, true},
		{"gated, single approver signed -> unlocked", 1, 1, false},
		{"gated, all signed -> unlocked", 2, 2, false},
		{"stray approvals on ungated state -> unlocked", 0, 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, approvalGateLocked(tc.required, tc.approved))
		})
	}
}

func TestBuildApproval(t *testing.T) {
	tests := []struct {
		name           string
		approvers      []string
		approved       []string
		caller         string
		wantStatus     string
		wantRequired   int
		wantApproved   int
		wantCanApprove bool
	}{
		{
			name:       "no approvers -> none, cannot approve",
			approvers:  nil,
			approved:   nil,
			caller:     "u1",
			wantStatus: "none",
		},
		{
			name:           "assigned approver who hasn't signed can approve",
			approvers:      []string{"u1", "u2"},
			approved:       nil,
			caller:         "u1",
			wantStatus:     "pending",
			wantRequired:   2,
			wantApproved:   0,
			wantCanApprove: true,
		},
		{
			name:           "assigned approver who already signed cannot approve again",
			approvers:      []string{"u1", "u2"},
			approved:       []string{"u1"},
			caller:         "u1",
			wantStatus:     "pending",
			wantRequired:   2,
			wantApproved:   1,
			wantCanApprove: false,
		},
		{
			name:           "non-approver sees pending but cannot approve",
			approvers:      []string{"u1", "u2"},
			approved:       []string{"u1"},
			caller:         "outsider",
			wantStatus:     "pending",
			wantRequired:   2,
			wantApproved:   1,
			wantCanApprove: false,
		},
		{
			name:           "empty caller (unresolved identity) cannot approve",
			approvers:      []string{"u1"},
			approved:       nil,
			caller:         "",
			wantStatus:     "pending",
			wantRequired:   1,
			wantCanApprove: false,
		},
		{
			name:           "fully approved -> approved, nobody can approve",
			approvers:      []string{"u1", "u2"},
			approved:       []string{"u1", "u2"},
			caller:         "u1",
			wantStatus:     "approved",
			wantRequired:   2,
			wantApproved:   2,
			wantCanApprove: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildApproval(tc.approvers, tc.approved, tc.caller)
			assert.Equal(t, tc.wantStatus, got.Status)
			assert.Equal(t, tc.wantRequired, got.Required)
			assert.Equal(t, tc.wantApproved, got.Approved)
			assert.Equal(t, tc.wantCanApprove, got.CanApprove)
			// Arrays must never be nil so JSON serializes [] not null.
			assert.NotNil(t, got.ApproverUserIDs)
			assert.NotNil(t, got.ApprovedUserIDs)
		})
	}
}

func TestContains(t *testing.T) {
	assert.True(t, contains([]string{"a", "b"}, "b"))
	assert.False(t, contains([]string{"a", "b"}, "c"))
	assert.False(t, contains(nil, "a"))
}
