//go:build dbtest

package approvalchain

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// rejectFixture is a credit memo sitting on its approval gate (Draft) with two
// configured approvers and one outsider -- credit memo is the fixture because
// it is one of the "flag in place" modules and has the fewest required columns
// to seed by hand (see seedCreditMemoWithOwner).
type rejectFixture struct {
	cfg           ModuleConfig
	internalID    int
	uuid          string
	recordTypeID  int
	draftStatusID int
	approverA     int
	approverB     int
	outsider      int
}

func seedRejectFixture(t *testing.T, pool *pgxpool.Pool) rejectFixture {
	t.Helper()
	ctx := context.Background()
	internalID, recordTypeID, draftStatusID, uuid, _, _, _, _ := seedCreditMemoWithOwner(t, pool)
	approverA, _, _, _ := seedActorEmployee(t, pool)
	approverB, _, _, _ := seedActorEmployee(t, pool)
	outsider, _, _, _ := seedActorEmployee(t, pool)

	cfg, ok := ForWorkflowKey("credit_memo")
	if !ok {
		t.Fatal("credit_memo must be registered")
	}
	reset := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM credit_memo_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, draftStatusID)
	}
	reset()
	t.Cleanup(reset)
	for _, emp := range []int{approverA, approverB} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO credit_memo_approver (record_type_id, record_status_id, approver_employee_id)
			VALUES ($1, $2, $3)`, recordTypeID, draftStatusID, emp); err != nil {
			t.Fatalf("seed credit_memo_approver: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE credit_memo SET credit_memo_approval_status = 'pending' WHERE credit_memo_id = $1`, internalID); err != nil {
		t.Fatalf("mark credit memo pending: %v", err)
	}
	return rejectFixture{cfg, internalID, uuid, recordTypeID, draftStatusID, approverA, approverB, outsider}
}

func (f rejectFixture) state(t *testing.T, pool *pgxpool.Pool) (statusID int, approvalStatus string, version int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT credit_memo_status, credit_memo_approval_status, credit_memo_record_version
		FROM credit_memo WHERE credit_memo_id = $1`, f.internalID).Scan(&statusID, &approvalStatus, &version); err != nil {
		t.Fatalf("load credit memo state: %v", err)
	}
	return statusID, approvalStatus, version
}

func (f rejectFixture) signOff(t *testing.T, pool *pgxpool.Pool, employeeID int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO credit_memo_approval (credit_memo_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.internalID, f.draftStatusID, employeeID); err != nil {
		t.Fatalf("seed sign-off: %v", err)
	}
}

func (f rejectFixture) signOffCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM credit_memo_approval WHERE credit_memo_id = $1`, f.internalID).Scan(&n); err != nil {
		t.Fatalf("count sign-offs: %v", err)
	}
	return n
}

// rejection reads the record's stored rejection row; found is false when the
// record has none.
func (f rejectFixture) rejection(t *testing.T, pool *pgxpool.Pool) (found bool, statusID int, rejectedBy *int, reason string) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT record_status_id, rejected_by, reason FROM approval_rejection
		WHERE record_type_id = $1 AND record_id = $2`, f.recordTypeID, f.internalID)
	if err != nil {
		t.Fatalf("query approval_rejection: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return false, 0, nil, ""
	}
	if err := rows.Scan(&statusID, &rejectedBy, &reason); err != nil {
		t.Fatalf("scan approval_rejection: %v", err)
	}
	return true, statusID, rejectedBy, reason
}

func (f rejectFixture) historyActions(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT action FROM credit_memo_history WHERE credit_memo_id = $1 ORDER BY credit_memo_history_id`, f.internalID)
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan history: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func TestReject_InPlace_FlagsTheRecordAndKeepsItsStatus(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	f.signOff(t, pool, f.approverB) // B already signed; A is about to veto
	_, _, versionBefore := f.state(t, pool)

	out, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, false, "  Wrong amount  ")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}

	if out.InternalID != f.internalID {
		t.Errorf("outcome InternalID = %d, want %d", out.InternalID, f.internalID)
	}
	if out.Reason != "Wrong amount" {
		t.Errorf("outcome Reason = %q, want the trimmed text a caller's own notification should carry", out.Reason)
	}
	if out.ReturnedToStatusCode != "" {
		t.Errorf("outcome ReturnedToStatusCode = %q, want empty for an in-place reject", out.ReturnedToStatusCode)
	}
	statusID, approvalStatus, version := f.state(t, pool)
	if statusID != f.draftStatusID {
		t.Errorf("status changed to %d, want it to stay %d", statusID, f.draftStatusID)
	}
	if approvalStatus != StatusRejected {
		t.Errorf("approval status = %q, want %q", approvalStatus, StatusRejected)
	}
	if version != versionBefore+1 {
		t.Errorf("record version = %d, want %d", version, versionBefore+1)
	}
	if n := f.signOffCount(t, pool); n != 0 {
		t.Errorf("sign-offs left = %d, want 0 so a resubmission starts a fresh round", n)
	}
	found, rejStatusID, rejectedBy, reason := f.rejection(t, pool)
	if !found {
		t.Fatal("no approval_rejection row stored")
	}
	if rejStatusID != f.draftStatusID {
		t.Errorf("rejection status = %d, want %d", rejStatusID, f.draftStatusID)
	}
	if rejectedBy == nil || *rejectedBy != f.approverA {
		t.Errorf("rejected_by = %v, want %d", rejectedBy, f.approverA)
	}
	if reason != "Wrong amount" {
		t.Errorf("reason = %q, want the trimmed text", reason)
	}
	if got := f.historyActions(t, pool); !reflect.DeepEqual(got, []string{"reject"}) {
		t.Errorf("history actions = %v, want [reject]", got)
	}
}

func TestReject_ToStatus_MovesTheRecordAndResetsApproval(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	// No registry module can be exercised for this branch inside this package
	// (the Draft-return modules import approvalchain), so borrow the credit
	// memo table and point its gate's reject at Void.
	cfg := f.cfg
	cfg.Gates = []Gate{{StatusCode: "DRFT", TargetStatusCode: "APPV", Reject: RejectToStatus, RejectStatusCode: "VOID"}}

	out, err := Reject(context.Background(), pool, cfg, f.uuid, f.approverA, false, "Not needed")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if out.ReturnedToStatusCode != "VOID" {
		t.Errorf("outcome ReturnedToStatusCode = %q, want VOID", out.ReturnedToStatusCode)
	}
	var voidStatusID int
	if err := pool.QueryRow(context.Background(), `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'VOID'`, f.recordTypeID).Scan(&voidStatusID); err != nil {
		t.Fatalf("resolve VOID: %v", err)
	}
	statusID, approvalStatus, _ := f.state(t, pool)
	if statusID != voidStatusID {
		t.Errorf("status = %d, want VOID (%d)", statusID, voidStatusID)
	}
	if approvalStatus != StatusNone {
		t.Errorf("approval status = %q, want %q after leaving the gate", approvalStatus, StatusNone)
	}
	found, rejStatusID, _, _ := f.rejection(t, pool)
	if !found || rejStatusID != voidStatusID {
		t.Errorf("rejection row found=%v status=%d, want it tied to the status the record was sent back to (%d)", found, rejStatusID, voidStatusID)
	}
}

func TestReject_RefusesACallerWhoIsNotAnApprover(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)

	_, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.outsider, false, "nope")
	if !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by an outsider = %v, want ErrNotApprover", err)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
		t.Errorf("approval status = %q, want it untouched (%q)", approvalStatus, StatusPending)
	}
	if found, _, _, _ := f.rejection(t, pool); found {
		t.Error("a refused Reject must not leave a rejection row behind")
	}
}

func TestReject_SuperAdminMayRejectWithoutBeingAnApprover(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)

	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.outsider, true, "Overruled"); err != nil {
		t.Fatalf("Reject by a super admin: %v", err)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusRejected {
		t.Errorf("approval status = %q, want %q", approvalStatus, StatusRejected)
	}
}

func TestReject_RequiresAReasonOfSaneLength(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)

	for _, tc := range []struct {
		name, reason string
		want         error
	}{
		{"empty", "", ErrReasonRequired},
		{"whitespace only", " \t\n ", ErrReasonRequired},
		{"too long", strings.Repeat("x", MaxRejectionReasonLength+1), ErrReasonTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, false, tc.reason); !errors.Is(err, tc.want) {
				t.Fatalf("Reject = %v, want %v", err, tc.want)
			}
			if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
				t.Errorf("approval status = %q, want it untouched", approvalStatus)
			}
		})
	}
}

func TestReject_RefusesARecordThatIsNotAwaitingApproval(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM credit_memo_approver WHERE record_type_id = $1 AND record_status_id = $2`, f.recordTypeID, f.draftStatusID); err != nil {
		t.Fatalf("unconfigure approvers: %v", err)
	}

	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, true, "x"); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Reject with no approvers configured = %v, want ErrApprovalNotRequired", err)
	}
}

func TestReject_RefusesAnUnsupportedGate(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	cfg := f.cfg
	cfg.Gates = []Gate{{StatusCode: "DRFT", TargetStatusCode: "APPV"}} // Reject left at its zero value

	if _, err := Reject(context.Background(), pool, cfg, f.uuid, f.approverA, false, "x"); !errors.Is(err, ErrRejectNotSupported) {
		t.Fatalf("Reject on a gate without a Reject mode = %v, want ErrRejectNotSupported", err)
	}
}

func TestReject_RefusesAnAlreadyRejectedRecord(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, false, "first"); err != nil {
		t.Fatalf("first Reject: %v", err)
	}
	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverB, false, "second"); !errors.Is(err, ErrAlreadyRejected) {
		t.Fatalf("second Reject = %v, want ErrAlreadyRejected", err)
	}
}

func TestReject_UnknownRecord(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	if _, err := Reject(context.Background(), pool, f.cfg, "00000000-0000-0000-0000-000000000000", f.approverA, true, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing record = %v, want ErrNotFound", err)
	}
}

func TestApprove_RefusesARejectedRecord(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, false, "Wrong amount"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	for name, superAdmin := range map[string]bool{"approver": false, "super admin": true} {
		t.Run(name, func(t *testing.T) {
			if _, err := Approve(context.Background(), pool, f.cfg, f.uuid, f.approverB, superAdmin); !errors.Is(err, ErrAlreadyRejected) {
				t.Fatalf("Approve of a rejected record = %v, want ErrAlreadyRejected (it must be edited and resubmitted first)", err)
			}
		})
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusRejected {
		t.Errorf("approval status = %q, want it to stay %q", approvalStatus, StatusRejected)
	}
}

func TestGetInfo_ReportsWhoCanRejectAndTheRejectionOnceItHappened(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		caller     int
		superAdmin bool
		want       bool
	}{
		{"configured approver", f.approverA, false, true},
		{"super admin who is not an approver", f.outsider, true, true},
		{"outsider", f.outsider, false, false},
	} {
		info, err := GetInfo(ctx, pool, f.cfg, f.uuid, tc.caller, tc.superAdmin)
		if err != nil {
			t.Fatalf("%s: GetInfo: %v", tc.name, err)
		}
		if info.CanReject != tc.want {
			t.Errorf("%s: CanReject = %v, want %v", tc.name, info.CanReject, tc.want)
		}
		if info.Rejection != nil {
			t.Errorf("%s: Rejection = %+v before any reject, want nil", tc.name, info.Rejection)
		}
	}

	if _, err := Reject(ctx, pool, f.cfg, f.uuid, f.approverA, false, "Wrong amount"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	info, err := GetInfo(ctx, pool, f.cfg, f.uuid, f.approverB, false)
	if err != nil {
		t.Fatalf("GetInfo after reject: %v", err)
	}
	if !info.Gated {
		t.Error("a rejected in-place record must stay Gated so its status transitions stay blocked")
	}
	if info.CanApprove || info.CanReject {
		t.Errorf("CanApprove=%v CanReject=%v, want both false until the record is resubmitted", info.CanApprove, info.CanReject)
	}
	if info.Rejection == nil {
		t.Fatal("Rejection missing after a reject")
	}
	if info.Rejection.Reason != "Wrong amount" {
		t.Errorf("Rejection.Reason = %q", info.Rejection.Reason)
	}
	if info.Rejection.ByName != "Notify Test Actor" {
		t.Errorf("Rejection.ByName = %q, want the rejecting approver's name", info.Rejection.ByName)
	}
	if info.Rejection.At.IsZero() {
		t.Error("Rejection.At is zero")
	}
}

func TestGetInfo_HidesARejectionOnceTheRecordHasMovedOn(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	if _, err := Reject(ctx, pool, f.cfg, f.uuid, f.approverA, false, "Wrong amount"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	// A stale row must never paint a red banner on a record that is somewhere
	// else now: move the credit memo to Void behind the engine's back.
	if _, err := pool.Exec(ctx, `
		UPDATE credit_memo SET credit_memo_status = (
			SELECT record_status_id FROM lkp_record_status
			WHERE record_status_record_type = $2 AND record_status_code = 'VOID')
		WHERE credit_memo_id = $1`, f.internalID, f.recordTypeID); err != nil {
		t.Fatalf("void credit memo: %v", err)
	}
	info, err := GetInfo(ctx, pool, f.cfg, f.uuid, f.approverA, false)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.Rejection != nil {
		t.Errorf("Rejection = %+v for a record no longer in the status it was rejected in, want nil", info.Rejection)
	}
}

// resubmit runs ResubmitAfterEdit inside a transaction of its own and commits
// it, the way a module's Update would around its own edit.
func (f rejectFixture) resubmit(t *testing.T, pool *pgxpool.Pool) *Resubmission {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r, err := ResubmitAfterEdit(ctx, tx, f.cfg, f.internalID)
	if err != nil {
		t.Fatalf("ResubmitAfterEdit: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return r
}

func TestResubmitAfterEdit_ReopensARejectedRecord(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	ctx := context.Background()
	if _, err := Reject(ctx, pool, f.cfg, f.uuid, f.approverA, false, "Wrong amount"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	r := f.resubmit(t, pool)
	if r == nil || !r.NeedsApproval {
		t.Fatalf("resubmission = %+v, want one that needs approval", r)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
		t.Errorf("approval status = %q, want %q", approvalStatus, StatusPending)
	}
	if found, _, _, _ := f.rejection(t, pool); found {
		t.Error("the rejection must be cleared once the record is resubmitted")
	}
	info, err := GetInfo(ctx, pool, f.cfg, f.uuid, f.approverB, false)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if !info.CanApprove || !info.CanReject || info.Rejection != nil {
		t.Errorf("info = %+v, want the record open for approval again with no rejection", info)
	}
	// Approve works again, and the earlier round's sign-offs are gone -- one
	// approver alone must not complete a two-approver quorum.
	if _, err := Approve(ctx, pool, f.cfg, f.uuid, f.approverB, false); err != nil {
		t.Fatalf("Approve after resubmit: %v", err)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
		t.Errorf("after one of two sign-offs approval status = %q, want still %q", approvalStatus, StatusPending)
	}
}

func TestResubmitAfterEdit_IgnoresARecordThatWasNotRejected(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)

	if r := f.resubmit(t, pool); r != nil {
		t.Errorf("resubmission = %+v for a record that was never rejected, want nil", r)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusPending {
		t.Errorf("approval status = %q, want it untouched", approvalStatus)
	}
}

func TestResubmitAfterEdit_WithNoApproversLeftClearsTheFlag(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, false, "Wrong amount"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	// An admin removed every approver while the record sat rejected.
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM credit_memo_approver WHERE record_type_id = $1 AND record_status_id = $2`, f.recordTypeID, f.draftStatusID); err != nil {
		t.Fatalf("unconfigure approvers: %v", err)
	}

	r := f.resubmit(t, pool)
	if r == nil || r.NeedsApproval {
		t.Fatalf("resubmission = %+v, want one that needs no approval", r)
	}
	if _, approvalStatus, _ := f.state(t, pool); approvalStatus != StatusNone {
		t.Errorf("approval status = %q, want %q -- nothing is left to approve it", approvalStatus, StatusNone)
	}
}

func TestClearRejection_RemovesTheStoredRejection(t *testing.T) {
	pool := notifyTestPool(t)
	f := seedRejectFixture(t, pool)
	if _, err := Reject(context.Background(), pool, f.cfg, f.uuid, f.approverA, false, "Wrong amount"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if err := ClearRejection(context.Background(), pool, f.recordTypeID, f.internalID); err != nil {
		t.Fatalf("ClearRejection: %v", err)
	}
	if found, _, _, _ := f.rejection(t, pool); found {
		t.Error("rejection row still present after ClearRejection")
	}
}
