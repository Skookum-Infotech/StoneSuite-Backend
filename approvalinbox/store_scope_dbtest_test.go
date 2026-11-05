//go:build dbtest

package approvalinbox

import (
	"context"
	"testing"
	"time"

	"stonesuite-backend/authz"
)

func TestInbox_OwnScopeOnlyReturnsCallersRecords(t *testing.T) {
	pool := testPool(t)
	dana, other := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Other")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana)
	cust := seedCustomer(t, pool)
	_, mine := seedMemo(t, pool, cust, dana, time.Now())
	_, theirs := seedMemo(t, pool, cust, other, time.Now())

	p := listAll(t, pool, onlyCreditMemo(authz.ScopeOwn), Caller{EmployeeID: dana.employeeID}, ScopeMine)
	if _, ok := find(p, mine); !ok {
		t.Error("own scope must include the caller's own record")
	}
	if _, ok := find(p, theirs); ok {
		t.Error("own scope must not include someone else's record")
	}
}

func TestInbox_ScopeAllIsSuperAdminOnlyAndDropsApproverFilter(t *testing.T) {
	pool := testPool(t)
	dana, root, owner := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Root"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana)
	id, uuid := seedMemo(t, pool, seedCustomer(t, pool), owner, time.Now())
	signMemo(t, pool, id, dana)
	check := onlyCreditMemo(authz.ScopeAll)

	if _, err := List(context.Background(), pool, check, Caller{EmployeeID: dana.employeeID}, Params{Scope: ScopeAll, Limit: 10}, time.Now()); err == nil {
		t.Fatal("scope=all by a non-super-admin must be refused by the store too")
	}
	p := listAll(t, pool, check, Caller{EmployeeID: root.employeeID, IsSuperAdmin: true}, ScopeAll)
	it, ok := find(p, uuid)
	if !ok {
		t.Fatal("super admin scope=all must list a pending record regardless of approver/signed state")
	}
	if !it.CanApprove {
		t.Error("super admin may override-approve")
	}
}

func TestInbox_MultiGateFabricationJob(t *testing.T) {
	pool := testPool(t)
	tmplApprover, qcApprover, owner := seedPerson(t, pool, "Templater"), seedPerson(t, pool, "QC"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "fabrication_job_approver", "FJOB", "TMPL", tmplApprover)
	setApprovers(t, pool, "fabrication_job_approver", "FJOB", "QCPD", qcApprover)
	cust := seedCustomer(t, pool)
	ctx := context.Background()
	soType, soStatus := typeAndStatus(t, pool, "SORD", "DRFT")
	var soID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_created_by)
		VALUES ($1, $2, $3, 1) RETURNING sales_order_id`, soType, soStatus, cust).Scan(&soID); err != nil {
		t.Fatalf("seed sales order: %v", err)
	}
	jobAt := func(status string) string {
		typeID, statusID := typeAndStatus(t, pool, "FJOB", status)
		var uuid string
		if err := pool.QueryRow(ctx, `
			INSERT INTO fabrication_job (record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id,
			                             job_owner_id, fabrication_job_number, job_approval_status, fabrication_job_created_by)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', 1) RETURNING fabrication_job_uuid::text`,
			typeID, statusID, soID, cust, owner.employeeID, "FJOB-"+uniq()).Scan(&uuid); err != nil {
			t.Fatalf("seed fabrication job at %s: %v", status, err)
		}
		return uuid
	}
	atTmpl, atQC := jobAt("TMPL"), jobAt("QCPD")
	check := func(r authz.Resource, a authz.Action) authz.Decision {
		if r == authz.ResourceInstallation {
			return authz.Decision{Allowed: true, Scope: authz.ScopeAll}
		}
		return authz.Decision{}
	}

	p := listAll(t, pool, check, Caller{EmployeeID: tmplApprover.employeeID}, ScopeMine)
	if _, ok := find(p, atTmpl); !ok {
		t.Error("the TMPL approver must see the job at TMPL")
	}
	if _, ok := find(p, atQC); ok {
		t.Error("the TMPL approver must not see the job at QCPD")
	}
	it, ok := find(listAll(t, pool, check, Caller{EmployeeID: qcApprover.employeeID}, ScopeMine), atQC)
	if !ok {
		t.Fatal("the QCPD approver must see the job at QCPD")
	}
	if it.CanReject {
		t.Error("fabrication gates do not support Reject: canReject must be false")
	}
	if it.Domain != "sales" || it.RouteModule != "installation" {
		t.Errorf("route = %s/%s, want sales/installation", it.Domain, it.RouteModule)
	}
}

func TestInbox_KeysetPagingIsStableAcrossNewArrivals(t *testing.T) {
	pool := testPool(t)
	dana, owner := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana)
	cust := seedCustomer(t, pool)
	base := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	want := map[string]bool{}
	// 7 memos, with deliberate entered_at ties so the (workflow_key, uuid) tiebreak is exercised.
	for i := 0; i < 7; i++ {
		_, u := seedMemo(t, pool, cust, owner, base.Add(time.Duration(i/2)*time.Hour))
		want[u] = true
	}
	check := onlyCreditMemo(authz.ScopeAll)
	caller := Caller{EmployeeID: dana.employeeID}

	seen := []Item{}
	var cursor *Cursor
	newcomer := ""
	for pageNo := 0; pageNo < 10; pageNo++ {
		p, err := List(context.Background(), pool, check, caller, Params{Scope: ScopeMine, Limit: 3, Cursor: cursor}, time.Now())
		if err != nil {
			t.Fatalf("page %d: %v", pageNo, err)
		}
		seen = append(seen, p.Items...)
		if pageNo == 0 {
			// A brand-new (newest) item arrives between pages: it sorts after everything seen so far.
			_, newcomer = seedMemo(t, pool, cust, owner, time.Now())
		}
		if !p.HasMore {
			break
		}
		c, err := DecodeCursor(p.NextCursor)
		if err != nil {
			t.Fatalf("nextCursor must decode: %v", err)
		}
		cursor = &c
	}
	dup := map[string]bool{}
	for i, it := range seen {
		if dup[it.RecordUUID] {
			t.Errorf("duplicate %s across pages", it.RecordUUID)
		}
		dup[it.RecordUUID] = true
		if i > 0 {
			prev := seen[i-1]
			if it.EnteredAt.Before(prev.EnteredAt) {
				t.Errorf("order broken at %d: %v before %v", i, it.EnteredAt, prev.EnteredAt)
			}
		}
	}
	for u := range want {
		if !dup[u] {
			t.Errorf("memo %s lost while paging", u)
		}
	}
	if !dup[newcomer] {
		t.Error("an item that arrived mid-paging sorts last and must still be reached, exactly once")
	}
}

func TestInbox_EmptyInboxShape(t *testing.T) {
	pool := testPool(t)
	nobody := seedPerson(t, pool, "Nobody")
	p := listAll(t, pool, onlyCreditMemo(authz.ScopeAll), Caller{EmployeeID: nobody.employeeID}, ScopeMine)
	if p.Items == nil || len(p.Items) != 0 || p.Total != 0 || p.HasMore || p.NextCursor != "" {
		t.Errorf("empty inbox = %+v, want items [] total 0 hasMore false", p)
	}
}

// The KPI tile calls Count; it must equal the inbox total for the same caller and scope.
func TestCountMatchesListTotal(t *testing.T) {
	pool := testPool(t)
	dana, bob, root, owner := seedPerson(t, pool, "Dana"), seedPerson(t, pool, "Bob"), seedPerson(t, pool, "Root"), seedPerson(t, pool, "Owner")
	setApprovers(t, pool, "credit_memo_approver", "CRDT", "DRFT", dana, bob)
	cust := seedCustomer(t, pool)
	for i := 0; i < 4; i++ {
		id, _ := seedMemo(t, pool, cust, owner, time.Now())
		if i%2 == 0 {
			signMemo(t, pool, id, dana)
		}
	}
	check := allowAllChecker
	for _, tc := range []struct {
		name   string
		caller Caller
		scope  Scope
	}{
		{"approver mine", Caller{EmployeeID: dana.employeeID}, ScopeMine},
		{"other approver mine", Caller{EmployeeID: bob.employeeID}, ScopeMine},
		{"super admin all", Caller{EmployeeID: root.employeeID, IsSuperAdmin: true}, ScopeAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts, _, err := CountWith(context.Background(), pool, check, tc.caller, tc.scope)
			if err != nil {
				t.Fatalf("CountWith: %v", err)
			}
			page := listAll(t, pool, check, tc.caller, tc.scope)
			if counts.Total != page.Total {
				t.Errorf("tile %d != inbox total %d", counts.Total, page.Total)
			}
			sum := 0
			for _, m := range page.Modules {
				sum += m.Count
			}
			if sum != page.Total {
				t.Errorf("module counts sum %d != total %d", sum, page.Total)
			}
		})
	}
}

// Every module's generated SQL must be valid Postgres against the real schema (column names are
// the part most likely to drift), including estimate/quote/sales_order's back-filled columns.
func TestEveryModuleSQLExecutes(t *testing.T) {
	pool := testPool(t)
	me := seedPerson(t, pool, "Everyone")
	for _, key := range Keys() {
		for _, own := range []bool{false, true} {
			src := []Source{{Key: key, OwnOnly: own}}
			for _, scope := range []Scope{ScopeMine, ScopeAll} {
				sql, args, err := buildPage(pageQuery{Sources: src, Scope: scope, EmployeeID: me.employeeID, Limit: 5})
				if err != nil {
					t.Fatalf("%s: build page: %v", key, err)
				}
				rows, err := pool.Query(context.Background(), sql, args...)
				if err != nil {
					t.Fatalf("%s own=%v scope=%s: page SQL rejected: %v", key, own, scope, err)
				}
				rows.Close()
				csql, cargs, _ := buildCount(src, scope, me.employeeID)
				crows, err := pool.Query(context.Background(), csql, cargs...)
				if err != nil {
					t.Fatalf("%s own=%v scope=%s: count SQL rejected: %v", key, own, scope, err)
				}
				crows.Close()
			}
		}
	}
	all := make([]Source, 0, len(Keys()))
	for _, k := range Keys() {
		all = append(all, Source{Key: k})
	}
	if _, err := isApproverAnywhere(context.Background(), pool, all, me.employeeID); err != nil {
		t.Fatalf("isApproverAnywhere SQL rejected: %v", err)
	}
}
