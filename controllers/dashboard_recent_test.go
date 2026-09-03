package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/estimate"
	"stonesuite-backend/expense"
	"stonesuite-backend/invoice"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/payment"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/query"
	"stonesuite-backend/quote"
	"stonesuite-backend/refund"
	"stonesuite-backend/requisition"
	"stonesuite-backend/salesorder"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/vendorcredit"
	"stonesuite-backend/vendorpayment"
	"stonesuite-backend/workflow"
)

func TestRecentRecords_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/recent-records/data", nil)
	rr := httptest.NewRecorder()
	h.RecentRecords(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRecentRecords_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/recent-records/data", nil)
	rr := httptest.NewRecorder()
	h.RecentRecords(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestUpdatedAtSinceFilter(t *testing.T) {
	t.Run("zero time is unbounded -- no filter clause", func(t *testing.T) {
		got := updatedAtSinceFilter(time.Time{})
		if got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("non-zero time narrows to updated_at >= since as RFC3339", func(t *testing.T) {
		since := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
		got := updatedAtSinceFilter(since)
		want := []query.Clause{{Field: "updated_at", Op: query.OpGte, Value: since.Format(time.RFC3339)}}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

func mkRecent(id string, minutesAgo int) recentRecord {
	return recentRecord{
		ID: id, Module: "sales_order", Domain: "sales", RecordNumber: id,
		Status: "Fabrication", UpdatedAt: time.Now().Add(-time.Duration(minutesAgo) * time.Minute),
	}
}

func TestMergeRecentRecords(t *testing.T) {
	t.Run("empty pool", func(t *testing.T) {
		records, hasMore := mergeRecentRecords(nil, 6)
		if len(records) != 0 || hasMore {
			t.Fatalf("records=%v hasMore=%v, want empty/false", records, hasMore)
		}
	})

	t.Run("sorts newest-updated-first across sources", func(t *testing.T) {
		perSource := [][]recentRecord{
			{mkRecent("a", 30), mkRecent("b", 5)},
			{mkRecent("c", 15)},
		}
		records, hasMore := mergeRecentRecords(perSource, 6)
		if hasMore {
			t.Fatal("hasMore = true, want false (only 3 rows, limit 6)")
		}
		gotOrder := []string{records[0].ID, records[1].ID, records[2].ID}
		wantOrder := []string{"b", "c", "a"} // 5m ago, 15m ago, 30m ago
		for i := range wantOrder {
			if gotOrder[i] != wantOrder[i] {
				t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
			}
		}
	})

	t.Run("truncates to limit and reports hasMore", func(t *testing.T) {
		perSource := [][]recentRecord{
			{mkRecent("a", 1), mkRecent("b", 2), mkRecent("c", 3), mkRecent("d", 4)},
		}
		records, hasMore := mergeRecentRecords(perSource, 2)
		if len(records) != 2 {
			t.Fatalf("len(records) = %d, want 2", len(records))
		}
		if !hasMore {
			t.Fatal("hasMore = false, want true (4 rows truncated to 2)")
		}
		if records[0].ID != "a" || records[1].ID != "b" {
			t.Fatalf("records = %v, want [a b] (newest first)", records)
		}
	})

	t.Run("pool exactly at limit is not hasMore", func(t *testing.T) {
		perSource := [][]recentRecord{{mkRecent("a", 1), mkRecent("b", 2)}}
		records, hasMore := mergeRecentRecords(perSource, 2)
		if len(records) != 2 || hasMore {
			t.Fatalf("records=%v hasMore=%v, want 2 rows/false", records, hasMore)
		}
	})
}

func TestBuildRecentRecords_AllSourcesGranted(t *testing.T) {
	sources := []recentSource{
		{module: "lead", domain: "crm", resource: authz.ResourceLead},
		{module: "sales_order", domain: "sales", resource: authz.ResourceSalesOrder},
	}
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	fetchAll := func(granted []grantedSource) ([][]recentRecord, error) {
		if len(granted) != 2 {
			t.Fatalf("fetchAll got %d granted sources, want 2", len(granted))
		}
		return [][]recentRecord{
			{mkRecent("lead-1", 10)},
			{mkRecent("so-1", 5)},
		}, nil
	}

	records, hasMore, ok, err := buildRecentRecords(sources, check, fetchAll, 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if hasMore {
		t.Fatal("hasMore = true, want false")
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
}

func TestBuildRecentRecords_PartialGrantSkipsUngranted(t *testing.T) {
	sources := []recentSource{
		{module: "lead", domain: "crm", resource: authz.ResourceLead},
		{module: "vendor_bill", domain: "purchases", resource: authz.ResourceVendorBill},
	}
	check := func(resource authz.Resource) (authz.Decision, error) {
		if resource == authz.ResourceVendorBill {
			return authz.Decision{Allowed: false}, nil
		}
		return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}, nil
	}
	fetchAll := func(granted []grantedSource) ([][]recentRecord, error) {
		if len(granted) != 1 {
			t.Fatalf("fetchAll got %d granted sources, want 1 (vendor_bill ungranted)", len(granted))
		}
		if granted[0].source.module != "lead" {
			t.Fatalf("granted source = %q, want lead", granted[0].source.module)
		}
		return [][]recentRecord{{mkRecent("lead-1", 1)}}, nil
	}

	_, _, ok, err := buildRecentRecords(sources, check, fetchAll, 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true (lead still granted)")
	}
}

func TestBuildRecentRecords_NoSourceGranted(t *testing.T) {
	sources := []recentSource{{module: "lead", domain: "crm", resource: authz.ResourceLead}}
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{Allowed: false}, nil
	}
	fetchAll := func(granted []grantedSource) ([][]recentRecord, error) {
		t.Fatal("fetchAll should not be called when nothing is granted")
		return nil, nil
	}

	records, hasMore, ok, err := buildRecentRecords(sources, check, fetchAll, 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("ok = true, want false; records=%v hasMore=%v", records, hasMore)
	}
}

func TestBuildRecentRecords_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	sources := []recentSource{{module: "lead", domain: "crm", resource: authz.ResourceLead}}
	check := func(resource authz.Resource) (authz.Decision, error) { return authz.Decision{}, boom }
	fetchAll := func(granted []grantedSource) ([][]recentRecord, error) { return nil, nil }

	_, _, _, err := buildRecentRecords(sources, check, fetchAll, 6)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildRecentRecords_PropagatesFetchError(t *testing.T) {
	boom := errors.New("boom")
	sources := []recentSource{{module: "lead", domain: "crm", resource: authz.ResourceLead}}
	check := func(resource authz.Resource) (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	fetchAll := func(granted []grantedSource) ([][]recentRecord, error) { return nil, boom }

	_, _, _, err := buildRecentRecords(sources, check, fetchAll, 6)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// ----- per-module mapper functions -----------------------------------------
//
// Each module's Search returns a differently-named, differently-shaped
// record type (see the respective package's types.go) -- there is no shared
// interface to table-drive across, so each mapper gets its own subtest
// constructing a minimal fixture of that module's real type.

func TestModuleMappers(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	t.Run("crm record uses CoreFields for account/status, nil value", func(t *testing.T) {
		rec := workflow.Record{
			ID: "lead-1", RecordNumber: "LEAD-1084", UpdatedAt: now,
			CoreFields: map[string]any{"customer_name": "Whitmore Residence", "crm_status_name": "New"},
		}
		got := crmRecordToRecent(rec)
		want := recentRecord{
			ID: "lead-1", RecordNumber: "LEAD-1084", Account: strPtr("Whitmore Residence"),
			Status: "New", Value: nil, UpdatedAt: now,
		}
		assertRecent(t, got, want)
	})

	t.Run("crm record with missing core field falls back to empty string, not a panic", func(t *testing.T) {
		rec := workflow.Record{ID: "lead-2", RecordNumber: "LEAD-2", UpdatedAt: now, CoreFields: map[string]any{}}
		got := crmRecordToRecent(rec)
		if got.Account == nil || *got.Account != "" {
			t.Errorf("Account = %v, want empty string pointer", got.Account)
		}
		if got.Status != "" {
			t.Errorf("Status = %q, want empty", got.Status)
		}
	})

	t.Run("quote", func(t *testing.T) {
		q := quote.Quote{ID: "q1", Number: "QUOT-1", Status: "Draft", Customer: quote.CustomerRef{Name: "Acme"}, GrandTotal: 500, UpdatedAt: now}
		assertRecent(t, quoteToRecent(q), recentRecord{ID: "q1", RecordNumber: "QUOT-1", Account: strPtr("Acme"), Value: floatPtr(500), Status: "Draft", UpdatedAt: now})
	})

	t.Run("estimate", func(t *testing.T) {
		e := estimate.Estimate{ID: "e1", Number: "ESTM-1", Status: "Sent", Customer: estimate.CustomerRef{Name: "Acme"}, GrandTotal: 200, UpdatedAt: now}
		assertRecent(t, estimateToRecent(e), recentRecord{ID: "e1", RecordNumber: "ESTM-1", Account: strPtr("Acme"), Value: floatPtr(200), Status: "Sent", UpdatedAt: now})
	})

	t.Run("sales order", func(t *testing.T) {
		o := salesorder.Order{ID: "so1", Number: "SO-1042", Status: "Fabrication", Customer: salesorder.CustomerRef{Name: "Fontaine Builders"}, GrandTotal: 28400, UpdatedAt: now}
		assertRecent(t, salesOrderToRecent(o), recentRecord{ID: "so1", RecordNumber: "SO-1042", Account: strPtr("Fontaine Builders"), Value: floatPtr(28400), Status: "Fabrication", UpdatedAt: now})
	})

	t.Run("invoice", func(t *testing.T) {
		inv := invoice.Invoice{ID: "inv1", Number: "INV-1", StatusName: "Sent", Customer: invoice.CustomerRef{Name: "Acme"}, GrandTotal: 900, UpdatedAt: now}
		assertRecent(t, invoiceToRecent(inv), recentRecord{ID: "inv1", RecordNumber: "INV-1", Account: strPtr("Acme"), Value: floatPtr(900), Status: "Sent", UpdatedAt: now})
	})

	t.Run("payment", func(t *testing.T) {
		p := payment.Payment{ID: "p1", Number: "PMT-1", StatusName: "Completed", Customer: payment.CustomerRef{Name: "Acme"}, Amount: 300, UpdatedAt: now}
		assertRecent(t, paymentToRecent(p), recentRecord{ID: "p1", RecordNumber: "PMT-1", Account: strPtr("Acme"), Value: floatPtr(300), Status: "Completed", UpdatedAt: now})
	})

	t.Run("credit memo", func(t *testing.T) {
		cm := creditmemo.CreditMemo{ID: "cm1", Number: "CM-1", StatusName: "Issued", Customer: creditmemo.CustomerRef{Name: "Acme"}, GrandTotal: 150, UpdatedAt: now}
		assertRecent(t, creditMemoToRecent(cm), recentRecord{ID: "cm1", RecordNumber: "CM-1", Account: strPtr("Acme"), Value: floatPtr(150), Status: "Issued", UpdatedAt: now})
	})

	t.Run("refund", func(t *testing.T) {
		rf := refund.Refund{ID: "rf1", Number: "RF-1", StatusName: "Processed", Customer: refund.CustomerRef{Name: "Acme"}, Amount: 75, UpdatedAt: now}
		assertRecent(t, refundToRecent(rf), recentRecord{ID: "rf1", RecordNumber: "RF-1", Account: strPtr("Acme"), Value: floatPtr(75), Status: "Processed", UpdatedAt: now})
	})

	t.Run("requisition with a vendor assigned", func(t *testing.T) {
		r := requisition.Requisition{ID: "req1", Number: "REQ-118", Status: "Pending", Vendor: &requisition.VendorRef{Name: "Apex Stone Supply"}, EstimatedTotal: 4780, UpdatedAt: now}
		assertRecent(t, requisitionToRecent(r), recentRecord{ID: "req1", RecordNumber: "REQ-118", Account: strPtr("Apex Stone Supply"), Value: floatPtr(4780), Status: "Pending", UpdatedAt: now})
	})

	t.Run("requisition with no vendor yet has nil account, not a panic", func(t *testing.T) {
		r := requisition.Requisition{ID: "req2", Number: "REQ-119", Status: "Draft", Vendor: nil, EstimatedTotal: 0, UpdatedAt: now}
		got := requisitionToRecent(r)
		if got.Account != nil {
			t.Errorf("Account = %v, want nil", got.Account)
		}
	})

	t.Run("purchase order", func(t *testing.T) {
		po := purchaseorder.PurchaseOrder{ID: "po1", Number: "PO-1", Status: "Approved", Vendor: purchaseorder.VendorRef{Name: "Apex Stone Supply"}, GrandTotal: 5000, UpdatedAt: now}
		assertRecent(t, purchaseOrderToRecent(po), recentRecord{ID: "po1", RecordNumber: "PO-1", Account: strPtr("Apex Stone Supply"), Value: floatPtr(5000), Status: "Approved", UpdatedAt: now})
	})

	t.Run("item receipt has no monetary total", func(t *testing.T) {
		ir := itemreceipt.ItemReceipt{ID: "ir1", Number: "IR-1", Status: "Posted", Vendor: itemreceipt.VendorRef{Name: "Apex Stone Supply"}, UpdatedAt: now}
		got := itemReceiptToRecent(ir)
		assertRecent(t, got, recentRecord{ID: "ir1", RecordNumber: "IR-1", Account: strPtr("Apex Stone Supply"), Value: nil, Status: "Posted", UpdatedAt: now})
	})

	t.Run("vendor bill", func(t *testing.T) {
		vb := vendorbill.VendorBill{ID: "vb1", Number: "VB-2091", StatusName: "Approved", Vendor: vendorbill.VendorRef{Name: "Apex Stone Supply"}, GrandTotal: 9120, UpdatedAt: now}
		assertRecent(t, vendorBillToRecent(vb), recentRecord{ID: "vb1", RecordNumber: "VB-2091", Account: strPtr("Apex Stone Supply"), Value: floatPtr(9120), Status: "Approved", UpdatedAt: now})
	})

	t.Run("vendor payment", func(t *testing.T) {
		vp := vendorpayment.VendorPayment{ID: "vp1", Number: "VP-1", StatusName: "Completed", Vendor: vendorpayment.VendorRef{Name: "Apex Stone Supply"}, Amount: 600, UpdatedAt: now}
		assertRecent(t, vendorPaymentToRecent(vp), recentRecord{ID: "vp1", RecordNumber: "VP-1", Account: strPtr("Apex Stone Supply"), Value: floatPtr(600), Status: "Completed", UpdatedAt: now})
	})

	t.Run("vendor credit", func(t *testing.T) {
		vc := vendorcredit.VendorCredit{ID: "vc1", Number: "VC-1", StatusName: "Open", Vendor: vendorcredit.VendorRef{Name: "Apex Stone Supply"}, GrandTotal: 250, UpdatedAt: now}
		assertRecent(t, vendorCreditToRecent(vc), recentRecord{ID: "vc1", RecordNumber: "VC-1", Account: strPtr("Apex Stone Supply"), Value: floatPtr(250), Status: "Open", UpdatedAt: now})
	})

	t.Run("expense has neither customer nor vendor", func(t *testing.T) {
		ex := expense.Expense{ID: "ex1", Number: "EXP-1", Status: "Pending", Total: 120, UpdatedAt: now}
		got := expenseToRecent(ex)
		assertRecent(t, got, recentRecord{ID: "ex1", RecordNumber: "EXP-1", Account: nil, Value: floatPtr(120), Status: "Pending", UpdatedAt: now})
	})
}

// assertRecent compares every field the mappers are responsible for (Module/
// Domain are stamped later by buildRecentSources, not the mappers, so they're
// deliberately excluded here).
func assertRecent(t *testing.T, got, want recentRecord) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.RecordNumber != want.RecordNumber {
		t.Errorf("RecordNumber = %q, want %q", got.RecordNumber, want.RecordNumber)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
	if (got.Account == nil) != (want.Account == nil) || (got.Account != nil && *got.Account != *want.Account) {
		t.Errorf("Account = %v, want %v", got.Account, want.Account)
	}
	if (got.Value == nil) != (want.Value == nil) || (got.Value != nil && *got.Value != *want.Value) {
		t.Errorf("Value = %v, want %v", got.Value, want.Value)
	}
}
