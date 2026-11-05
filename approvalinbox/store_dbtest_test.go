//go:build dbtest

package approvalinbox

import (
	"context"
	"testing"
	"time"

	"stonesuite-backend/authz"
)

func TestInbox_ApproverSeesPendingWithQuorumAndFlags(t *testing.T) {
	pool := testPool(t)
	dana, bob, owner := seedPerson(t, pool, "Dana Lee"), seedPerson(t, pool, "Bob"), seedPerson(t, pool, "Olive Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana, bob)
	memoID, uuid := seedMemo(t, pool, seedCustomer(t, pool), owner, time.Now().Add(-72*time.Hour))
	_ = memoID

	p := listAll(t, pool, onlyCreditMemo(authz.ScopeAll), Caller{EmployeeID: dana.employeeID}, ScopeMine)
	it, ok := find(p, uuid)
	if !ok {
		t.Fatalf("approver must see the pending memo; got %d items", len(p.Items))
	}
	if !it.CanApprove || !it.CanReject {
		t.Errorf("canApprove/canReject = %v/%v, want true/true (credit memo rejects in place)", it.CanApprove, it.CanReject)
	}
	if it.RequiredApprovals != 2 || it.ApprovedCount != 0 {
		t.Errorf("quorum = %d/%d, want 0/2", it.ApprovedCount, it.RequiredApprovals)
	}
	if it.OwnerName != "Olive Owner" || it.Number == "" || it.StatusCode != "DRFT" || it.AgeDays < 2 {
		t.Errorf("projection wrong: %+v", it)
	}
	if it.Domain != "sales" || it.RouteModule != "credit_memo" || it.Amount == "" {
		t.Errorf("registry fields wrong: %+v", it)
	}
}

func TestInbox_SignedByCallerIsHiddenButOtherApproverStillSeesIt(t *testing.T) {
	pool := testPool(t)
	dana, bob, owner := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Bob"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana, bob)
	id, uuid := seedMemo(t, pool, seedCustomer(t, pool), owner, time.Now())
	signMemo(t, pool, id, dana)
	check := onlyCreditMemo(authz.ScopeAll)

	if _, ok := find(listAll(t, pool, check, Caller{EmployeeID: dana.employeeID}, ScopeMine), uuid); ok {
		t.Error("a record the caller already signed must not be in their inbox")
	}
	it, ok := find(listAll(t, pool, check, Caller{EmployeeID: bob.employeeID}, ScopeMine), uuid)
	if !ok {
		t.Fatal("the other approver must still see it")
	}
	if it.ApprovedCount != 1 || it.RequiredApprovals != 2 {
		t.Errorf("quorum = %d/%d, want 1/2", it.ApprovedCount, it.RequiredApprovals)
	}
}

func TestInbox_ExcludesNonApproverRejectedDeletedAndNotPending(t *testing.T) {
	pool := testPool(t)
	dana, outsider, owner := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Outsider"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana)
	cust := seedCustomer(t, pool)
	_, live := seedMemo(t, pool, cust, owner, time.Now())
	rejectedID, rejected := seedMemo(t, pool, cust, owner, time.Now())
	deletedID, deleted := seedMemo(t, pool, cust, owner, time.Now())
	noneID, none := seedMemo(t, pool, cust, owner, time.Now())
	ctx := context.Background()
	for q, id := range map[string]int{
		`UPDATE credit_memo SET credit_memo_approval_status = 'rejected' WHERE credit_memo_id = $1`:                   rejectedID,
		`UPDATE credit_memo SET credit_memo_deleted_at = NOW(), credit_memo_deleted_by = 1 WHERE credit_memo_id = $1`: deletedID,
		`UPDATE credit_memo SET credit_memo_approval_status = 'none' WHERE credit_memo_id = $1`:                       noneID,
	} {
		if _, err := pool.Exec(ctx, q, id); err != nil {
			t.Fatalf("prepare: %v", err)
		}
	}
	check := onlyCreditMemo(authz.ScopeAll)

	p := listAll(t, pool, check, Caller{EmployeeID: dana.employeeID}, ScopeMine)
	if _, ok := find(p, live); !ok {
		t.Fatal("the live pending memo must be listed")
	}
	for name, u := range map[string]string{"rejected": rejected, "deleted": deleted, "not pending": none} {
		if _, ok := find(p, u); ok {
			t.Errorf("%s memo must not be listed", name)
		}
	}
	if _, ok := find(listAll(t, pool, check, Caller{EmployeeID: outsider.employeeID}, ScopeMine), live); ok {
		t.Error("a non-approver must not see it in scope mine")
	}
	if got := listAll(t, pool, check, Caller{EmployeeID: 0}, ScopeMine); len(got.Items) != 0 {
		t.Errorf("a caller with no employee must get an empty inbox, got %d", len(got.Items))
	}
}

func TestInbox_RBACDeniedModuleIsAbsentEverywhere(t *testing.T) {
	pool := testPool(t)
	dana, owner := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana)
	_, uuid := seedMemo(t, pool, seedCustomer(t, pool), owner, time.Now())

	deny := func(r authz.Resource, a authz.Action) authz.Decision {
		if r == authz.ResourceCreditMemo {
			return authz.Decision{}
		}
		return allowAllChecker(r, a)
	}
	p := listAll(t, pool, deny, Caller{EmployeeID: dana.employeeID}, ScopeMine)
	if _, ok := find(p, uuid); ok {
		t.Error("credit_memo read denied: no credit memo rows")
	}
	for _, m := range p.Modules {
		if m.WorkflowKey == "credit_memo" {
			t.Error("credit_memo read denied: no entry in modules")
		}
	}
}
