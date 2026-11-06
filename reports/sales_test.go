package reports

import (
	"context"
	"strings"
	"testing"
	"time"

	"stonesuite-backend/authz"
)

func TestParseBasis(t *testing.T) {
	cases := []struct {
		in   string
		want Basis
		ok   bool
	}{
		{"", BasisInvoices, true},
		{"invoices", BasisInvoices, true},
		{"orders", BasisOrders, true},
		{"quotes", "", false},
	}
	for _, c := range cases {
		got, ok := ParseBasis(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseBasis(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIsUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0b2f6f6e-8c1d-4e0a-9f3b-1a2b3c4d5e6f", true},
		{"0B2F6F6E-8C1D-4E0A-9F3B-1A2B3C4D5E6F", true},
		{"", false},
		{"not-a-uuid", false},
		{"0b2f6f6e8c1d4e0a9f3b1a2b3c4d5e6f", false},
		{"0b2f6f6e-8c1d-4e0a-9f3b-1a2b3c4d5e6f'; DROP", false},
	}
	for _, c := range cases {
		if got := IsUUID(c.in); got != c.want {
			t.Errorf("IsUUID(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestParseBucket(t *testing.T) {
	cases := []struct {
		in   string
		want Bucket
		ok   bool
	}{
		{"", BucketMonth, true},
		{"month", BucketMonth, true},
		{"week", BucketWeek, true},
		{"day", "", false},
	}
	for _, c := range cases {
		got, ok := ParseBucket(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseBucket(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNewTotals(t *testing.T) {
	cases := []struct {
		name            string
		revenue, cost   float64
		wantMargin      float64
		wantMarginPcent float64
	}{
		{"profit", 200, 150, 50, 25},
		{"loss", 100, 130, -30, -30},
		{"no revenue has no percent", 0, 40, -40, 0},
	}
	for _, c := range cases {
		got := newTotals(c.revenue, c.cost, 3)
		if got.Margin != c.wantMargin || got.MarginPc != c.wantMarginPcent || got.Records != 3 {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestSalesFromUsesBasisAndWindow(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		basis     Basis
		wantTable string
		wantDate  string
		wantCodes []string
	}{
		{BasisOrders, "FROM sales_order h", "h.sales_order_date >= $2", []string{"OPEN", "PART", "FILL"}},
		{BasisInvoices, "FROM invoice h", "h.invoice_date >= $2", []string{"SENT", "PART", "ODUE", "PAID"}},
	}
	for _, c := range cases {
		sql, args, ok, err := salesFrom(context.Background(), nil, Filter{
			Basis: c.basis, From: from, To: to, Scope: authz.ScopeAll,
		})
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", c.basis, ok, err)
		}
		if !strings.Contains(sql, c.wantTable) || !strings.Contains(sql, c.wantDate) {
			t.Errorf("%s: SQL missing %q / %q:\n%s", c.basis, c.wantTable, c.wantDate, sql)
		}
		codes, _ := args[0].([]string)
		if strings.Join(codes, ",") != strings.Join(c.wantCodes, ",") {
			t.Errorf("%s: status codes = %v want %v", c.basis, codes, c.wantCodes)
		}
		// $1 codes, $2 from, $3 to+1 day (To is inclusive).
		if len(args) != 3 || !args[2].(time.Time).Equal(to.AddDate(0, 0, 1)) {
			t.Errorf("%s: args = %v", c.basis, args)
		}
	}
}

func TestSalesFromUnboundedWindowHasNoDateArgs(t *testing.T) {
	_, args, ok, err := salesFrom(context.Background(), nil, Filter{Basis: BasisInvoices, Scope: authz.ScopeAll})
	if err != nil || !ok || len(args) != 1 {
		t.Fatalf("ok=%v err=%v args=%v", ok, err, args)
	}
}

func TestDrillRecordsRequiresSelector(t *testing.T) {
	_, err := DrillRecords(context.Background(), nil, Filter{Basis: BasisInvoices, Scope: authz.ScopeAll}, DrillSelector{})
	if err == nil {
		t.Fatal("expected an error when no customer, item or period is given")
	}
}
