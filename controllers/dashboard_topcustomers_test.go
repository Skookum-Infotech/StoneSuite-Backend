package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
)

func TestTopCustomers_RequiresAuth(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets/top-customers/data", nil)
	rr := httptest.NewRecorder()
	h.TopCustomers(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestTopCustomers_RejectsWrongMethod(t *testing.T) {
	h := NewDashboardUIOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/dashboard/widgets/top-customers/data", nil)
	rr := httptest.NewRecorder()
	h.TopCustomers(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestMapTopCustomers(t *testing.T) {
	rows := []invoice.TopCustomer{
		{CustomerUUID: "cust-1", Name: "Fontaine Builders", Value: 142300},
		{CustomerUUID: "cust-2", Name: "Sterling Kitchen & Bath", Value: 96500},
	}

	t.Run("populates id when linkCustomerID is true", func(t *testing.T) {
		out := mapTopCustomers(rows, nil, true)
		if len(out) != 2 {
			t.Fatalf("len = %d, want 2", len(out))
		}
		if out[0].ID == nil || *out[0].ID != "cust-1" {
			t.Errorf("out[0].ID = %v, want cust-1", out[0].ID)
		}
	})

	t.Run("omits id when linkCustomerID is false", func(t *testing.T) {
		out := mapTopCustomers(rows, nil, false)
		if out[0].ID != nil {
			t.Errorf("out[0].ID = %v, want nil", out[0].ID)
		}
	})

	t.Run("omits priorValue entirely when prior is nil (no applicable prior window)", func(t *testing.T) {
		out := mapTopCustomers(rows, nil, true)
		for i, c := range out {
			if c.PriorValue != nil {
				t.Errorf("out[%d].PriorValue = %v, want nil", i, *c.PriorValue)
			}
		}
	})

	t.Run("reads a real prior value from the map when present", func(t *testing.T) {
		prior := map[string]float64{"cust-1": 120600}
		out := mapTopCustomers(rows, prior, true)
		if out[0].PriorValue == nil || *out[0].PriorValue != 120600 {
			t.Errorf("out[0].PriorValue = %v, want 120600", out[0].PriorValue)
		}
	})

	t.Run("reads a zero prior value (not nil) for a customer absent from a non-nil prior map -- a real new-this-period signal", func(t *testing.T) {
		prior := map[string]float64{"cust-1": 120600} // cust-2 absent
		out := mapTopCustomers(rows, prior, true)
		if out[1].PriorValue == nil {
			t.Fatal("out[1].PriorValue = nil, want a real zero (customer billed nothing last period, not \"not applicable\")")
		}
		if *out[1].PriorValue != 0 {
			t.Errorf("out[1].PriorValue = %v, want 0", *out[1].PriorValue)
		}
	})
}

func TestBuildTopCustomers_Granted(t *testing.T) {
	current := invoice.TopCustomersResult{
		Customers:     []invoice.TopCustomer{{CustomerUUID: "cust-1", Name: "Fontaine Builders", Value: 142300}},
		TotalValue:    638800,
		CustomerCount: 23,
	}
	check := func() (authz.Decision, error) {
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil
	}
	var gotScope authz.Scope
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) {
		gotScope = scope
		return current, nil
	}
	var gotPriorUUIDs []string
	fetchPrior := func(scope authz.Scope, customerUUIDs []string) (map[string]float64, error) {
		gotPriorUUIDs = customerUUIDs
		return map[string]float64{"cust-1": 120600}, nil
	}

	result, ok, err := buildTopCustomers(check, fetchCurrent, fetchPrior, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if gotScope != authz.ScopeAll {
		t.Errorf("fetchCurrent called with scope %q, want %q", gotScope, authz.ScopeAll)
	}
	if len(gotPriorUUIDs) != 1 || gotPriorUUIDs[0] != "cust-1" {
		t.Errorf("fetchPrior called with %v, want [cust-1]", gotPriorUUIDs)
	}
	if result.TotalValue != 638800 || result.CustomerCount != 23 {
		t.Errorf("TotalValue/CustomerCount = %v/%v, want 638800/23", result.TotalValue, result.CustomerCount)
	}
	if len(result.Customers) != 1 || result.Customers[0].PriorValue == nil || *result.Customers[0].PriorValue != 120600 {
		t.Errorf("Customers = %+v, want 1 row with PriorValue 120600", result.Customers)
	}
}

func TestBuildTopCustomers_NilFetchPriorOmitsEveryPriorValue(t *testing.T) {
	current := invoice.TopCustomersResult{
		Customers:  []invoice.TopCustomer{{CustomerUUID: "cust-1", Name: "Fontaine Builders", Value: 142300}},
		TotalValue: 142300, CustomerCount: 1,
	}
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) { return current, nil }

	result, ok, err := buildTopCustomers(check, fetchCurrent, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if result.Customers[0].PriorValue != nil {
		t.Errorf("PriorValue = %v, want nil (fetchPrior was nil -- range has no applicable prior window)", result.Customers[0].PriorValue)
	}
}

func TestBuildTopCustomers_SkipsFetchPriorWhenNoCurrentCustomers(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) { return invoice.TopCustomersResult{}, nil }
	fetchPrior := func(scope authz.Scope, customerUUIDs []string) (map[string]float64, error) {
		t.Fatal("fetchPrior should not be called when there are no current-window customers to look up")
		return nil, nil
	}

	result, ok, err := buildTopCustomers(check, fetchCurrent, fetchPrior, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(result.Customers) != 0 {
		t.Errorf("Customers = %+v, want empty", result.Customers)
	}
}

func TestBuildTopCustomers_NotGranted(t *testing.T) {
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: false}, nil }
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) {
		t.Fatal("fetchCurrent should not be called when the caller holds no grant")
		return invoice.TopCustomersResult{}, nil
	}

	_, ok, err := buildTopCustomers(check, fetchCurrent, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (no grant)")
	}
}

func TestBuildTopCustomers_PropagatesCheckError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{}, boom }
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) {
		t.Fatal("fetchCurrent should not be called when check errors")
		return invoice.TopCustomersResult{}, nil
	}

	_, _, err := buildTopCustomers(check, fetchCurrent, nil, true)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildTopCustomers_PropagatesFetchCurrentError(t *testing.T) {
	boom := errors.New("boom")
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}, nil }
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) {
		return invoice.TopCustomersResult{}, boom
	}

	_, _, err := buildTopCustomers(check, fetchCurrent, nil, true)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestBuildTopCustomers_PropagatesFetchPriorError(t *testing.T) {
	boom := errors.New("boom")
	current := invoice.TopCustomersResult{Customers: []invoice.TopCustomer{{CustomerUUID: "cust-1"}}}
	check := func() (authz.Decision, error) { return authz.Decision{Allowed: true, Scope: authz.ScopeAll}, nil }
	fetchCurrent := func(scope authz.Scope) (invoice.TopCustomersResult, error) { return current, nil }
	fetchPrior := func(scope authz.Scope, customerUUIDs []string) (map[string]float64, error) {
		return nil, boom
	}

	_, _, err := buildTopCustomers(check, fetchCurrent, fetchPrior, true)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
