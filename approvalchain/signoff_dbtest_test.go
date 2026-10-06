//go:build dbtest

package approvalchain

import (
	"context"
	"testing"
)

// TestApprove_RemovedApproversEarlierVoteDoesNotCount covers approvers {A,B}
// where A votes, the config is replaced with {B,C}, and B then votes: A's vote
// no longer comes from an active approver, so 1 of 2 must not finalize.
func TestApprove_RemovedApproversEarlierVoteDoesNotCount(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	approverC, _, _, _ := seedActorEmployee(t, pool)

	f.signOff(t, pool, f.approverA)
	if _, err := pool.Exec(ctx,
		`DELETE FROM credit_memo_approver WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3`,
		f.recordTypeID, f.draftStatusID, f.approverA); err != nil {
		t.Fatalf("remove approver A: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credit_memo_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.recordTypeID, f.draftStatusID, approverC); err != nil {
		t.Fatalf("add approver C: %v", err)
	}

	if _, err := Approve(ctx, pool, f.cfg, f.uuid, f.approverB, false); err != nil {
		t.Fatalf("Approve by B: %v", err)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
		t.Fatalf("approval status = %q, want still %q: removed approver A's vote must not count", approvalStatus, StatusPending)
	}

	if _, err := Approve(ctx, pool, f.cfg, f.uuid, approverC, false); err != nil {
		t.Fatalf("Approve by C: %v", err)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus == StatusPending {
		t.Errorf("approval status still %q after both active approvers voted", approvalStatus)
	}
}
