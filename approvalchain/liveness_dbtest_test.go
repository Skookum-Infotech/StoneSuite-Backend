//go:build dbtest

package approvalchain

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// disableUser mirrors userstore.DeactivateUser: status flips, nothing else --
// the approver-config row stays is_active.
func disableUser(t *testing.T, pool *pgxpool.Pool, employeeID int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE users SET status = 'disabled'
		WHERE id = (SELECT employee_user_id FROM employee WHERE employee_id = $1)`, employeeID); err != nil {
		t.Fatalf("disable user for employee %d: %v", employeeID, err)
	}
}

func softDeleteEmployee(t *testing.T, pool *pgxpool.Pool, employeeID int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE employee SET employee_deleted_at = NOW(), employee_deleted_by = 1 WHERE employee_id = $1`, employeeID); err != nil {
		t.Fatalf("soft-delete employee %d: %v", employeeID, err)
	}
}

func (f rejectFixture) requiredNow(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	n, err := ActiveApproverCount(context.Background(), pool, f.cfg.ApproverTable, f.recordTypeID, f.draftStatusID)
	if err != nil {
		t.Fatalf("ActiveApproverCount: %v", err)
	}
	return n
}

func TestApprove_DisabledApproverDropsOutOfQuorum(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	disableUser(t, pool, f.approverB)

	if got := f.requiredNow(t, pool); got != 1 {
		t.Fatalf("required = %d, want 1 once B is deactivated", got)
	}
	info, err := GetInfo(ctx, pool, f.cfg, f.uuid, f.approverA, false)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if len(info.Approvers) != 1 || info.Orphaned {
		t.Fatalf("approvers = %+v orphaned = %v, want only A and not orphaned", info.Approvers, info.Orphaned)
	}
	if _, err := Approve(ctx, pool, f.cfg, f.uuid, f.approverB, false); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Approve by deactivated B: err = %v, want ErrNotApprover", err)
	}
	out, err := Approve(ctx, pool, f.cfg, f.uuid, f.approverA, false)
	if err != nil {
		t.Fatalf("Approve by A: %v", err)
	}
	if !out.Finalized {
		t.Error("A alone must finalize once B is deactivated")
	}
}

func TestApprove_DeletedEmployeeDropsOutOfQuorum(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	softDeleteEmployee(t, pool, f.approverB)

	if got := f.requiredNow(t, pool); got != 1 {
		t.Fatalf("required = %d, want 1 once B's employee is deleted", got)
	}
}

func TestApprove_SinceDisabledApproversVoteDoesNotCount(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	approverC, _, _, _ := seedActorEmployee(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO credit_memo_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.recordTypeID, f.draftStatusID, approverC); err != nil {
		t.Fatalf("add approver C: %v", err)
	}

	// A signs, then is deactivated: live approvers are {B, C}, live votes 0.
	f.signOff(t, pool, f.approverA)
	disableUser(t, pool, f.approverA)
	if _, err := Approve(ctx, pool, f.cfg, f.uuid, f.approverB, false); err != nil {
		t.Fatalf("Approve by B: %v", err)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
		t.Fatalf("approval status = %q, want still %q: deactivated A's vote must not count", approvalStatus, StatusPending)
	}
}

func TestApprove_OrphanedGateFailsClosedUntilSuperAdminOverride(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	disableUser(t, pool, f.approverA)
	disableUser(t, pool, f.approverB)

	if got := f.requiredNow(t, pool); got != 1 {
		t.Fatalf("required = %d, want 1: an orphaned gate must stay gated", got)
	}
	info, err := GetInfo(ctx, pool, f.cfg, f.uuid, f.outsider, false)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if !info.Gated || !info.Orphaned || info.CanApprove || len(info.Approvers) != 0 {
		t.Fatalf("info = %+v, want gated, orphaned, no approvers, not approvable", info)
	}
	if _, err := Approve(ctx, pool, f.cfg, f.uuid, f.approverA, false); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Approve by deactivated A: err = %v, want ErrNotApprover", err)
	}
	if _, err := Approve(ctx, pool, f.cfg, f.uuid, f.outsider, false); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Approve by outsider: err = %v, want ErrNotApprover", err)
	}
	out, err := Approve(ctx, pool, f.cfg, f.uuid, f.outsider, true)
	if err != nil {
		t.Fatalf("super admin override: %v", err)
	}
	if !out.Finalized || !out.Override {
		t.Errorf("outcome = %+v, want finalized override", out)
	}
}

func TestApproverContacts_SkipDeactivatedAndDeleted(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	approverC, _, _, _ := seedActorEmployee(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO credit_memo_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.recordTypeID, f.draftStatusID, approverC); err != nil {
		t.Fatalf("add approver C: %v", err)
	}
	disableUser(t, pool, f.approverB)
	softDeleteEmployee(t, pool, approverC)

	active, err := activeApproverContacts(ctx, pool, f.cfg.ApproverTable, f.recordTypeID, f.draftStatusID)
	if err != nil {
		t.Fatalf("activeApproverContacts: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active contacts = %+v, want only A", active)
	}
	remaining, err := remainingApproverContacts(ctx, pool, f.cfg.ApproverTable, f.cfg.ApprovalTable, f.cfg.Record.IDColumn, f.recordTypeID, f.draftStatusID, f.internalID)
	if err != nil {
		t.Fatalf("remainingApproverContacts: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("remaining contacts = %+v, want only A", remaining)
	}
}

func TestOrphanedGateContacts_NotifiesActiveSuperAdminsOnly(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()

	var roleID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO roles (key, name, is_system) VALUES ('super_admin', 'Super Admin', TRUE)
		ON CONFLICT ((LOWER(key))) DO UPDATE SET key = roles.key
		RETURNING id`).Scan(&roleID); err != nil {
		t.Fatalf("ensure super_admin role: %v", err)
	}
	liveAdmin, liveUserID, liveIdentity, _ := seedActorEmployee(t, pool)
	goneAdmin, goneUserID, goneIdentity, _ := seedActorEmployee(t, pool)
	for _, uid := range []string{liveUserID, goneUserID} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, uid, roleID); err != nil {
			t.Fatalf("grant super_admin: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = ANY($1::uuid[])`, []string{liveUserID, goneUserID})
	})
	disableUser(t, pool, goneAdmin)
	_ = liveAdmin

	ec := EventContext{ApproverTable: f.cfg.ApproverTable, RecordTypeID: f.recordTypeID, StatusID: f.draftStatusID}

	// Not orphaned yet: no fallback.
	contacts, err := orphanedGateContacts(ctx, pool, ec)
	if err != nil {
		t.Fatalf("orphanedGateContacts: %v", err)
	}
	if contacts != nil {
		t.Fatalf("contacts = %+v, want nil while live approvers remain", contacts)
	}

	disableUser(t, pool, f.approverA)
	disableUser(t, pool, f.approverB)
	contacts, err = orphanedGateContacts(ctx, pool, ec)
	if err != nil {
		t.Fatalf("orphanedGateContacts: %v", err)
	}
	ids := map[string]bool{}
	for _, c := range contacts {
		ids[c.UserID] = true
	}
	if !ids[liveIdentity] {
		t.Errorf("active super admin %s missing from %+v", liveIdentity, contacts)
	}
	if ids[goneIdentity] {
		t.Errorf("deactivated super admin %s must not be notified", goneIdentity)
	}
}
