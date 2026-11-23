package glpost

import (
	"errors"
	"testing"
)

// sums returns total debit and credit cents of legs.
func sums(legs []Leg) (dr, cr int64) {
	for _, l := range legs {
		dr += l.DebitCents
		cr += l.CreditCents
	}
	return dr, cr
}

func legFor(legs []Leg, slot string) (Leg, bool) {
	for _, l := range legs {
		if l.Slot == slot && (l.DebitCents != 0 || l.CreditCents != 0) {
			return l, true
		}
	}
	return Leg{}, false
}

func TestCents(t *testing.T) {
	tests := []struct {
		in   float64
		want int64
	}{
		{0, 0}, {12.34, 1234}, {12.340000000000002, 1234}, {0.1 + 0.2, 30}, {1e13 - 0.01, 999999999999999}, // the largest DECIMAL(15,2) amount, in cents
	}
	for _, tt := range tests {
		if got := Cents(tt.in); got != tt.want {
			t.Errorf("Cents(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestInvoiceLegs(t *testing.T) {
	tests := []struct {
		name                               string
		a                                  InvoiceAmounts
		wantAR, wantDisc, wantTax, wantRev int64
	}{
		// 100.00 subtotal, 10.00 discount, 9.00 tax, 5.00 shipping => 104.00 grand.
		{"discount, tax and shipping", InvoiceAmounts{Discount: 1000, Tax: 900, Grand: 10400}, 10400, 1000, 900, 10400 + 1000 - 900},
		{"plain invoice", InvoiceAmounts{Grand: 5000}, 5000, 0, 0, 5000},
		{"tax only", InvoiceAmounts{Tax: 700, Grand: 10700}, 10700, 0, 700, 10000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legs := InvoiceLegs(tt.a)
			dr, cr := sums(legs)
			if dr != cr {
				t.Fatalf("unbalanced: debit %d credit %d", dr, cr)
			}
			if l, _ := legFor(legs, SlotAR); l.DebitCents != tt.wantAR {
				t.Errorf("AR debit = %d, want %d", l.DebitCents, tt.wantAR)
			}
			if l, _ := legFor(legs, SlotSalesDiscount); l.DebitCents != tt.wantDisc {
				t.Errorf("discount debit = %d, want %d", l.DebitCents, tt.wantDisc)
			}
			if l, _ := legFor(legs, SlotTaxPayable); l.CreditCents != tt.wantTax {
				t.Errorf("tax credit = %d, want %d", l.CreditCents, tt.wantTax)
			}
			if l, _ := legFor(legs, SlotSalesRevenue); l.CreditCents != tt.wantRev {
				t.Errorf("revenue credit = %d, want %d", l.CreditCents, tt.wantRev)
			}
		})
	}
}

func TestBillLegs(t *testing.T) {
	legs := BillLegs(BillAmounts{Tax: 800, Grand: 10800})
	dr, cr := sums(legs)
	if dr != cr {
		t.Fatalf("unbalanced: debit %d credit %d", dr, cr)
	}
	if l, _ := legFor(legs, SlotAP); l.CreditCents != 10800 {
		t.Errorf("AP credit = %d, want 10800", l.CreditCents)
	}
	if l, _ := legFor(legs, SlotTaxReceivable); l.DebitCents != 800 {
		t.Errorf("tax receivable debit = %d, want 800", l.DebitCents)
	}
	if l, _ := legFor(legs, SlotCOGS); l.DebitCents != 10000 {
		t.Errorf("COGS debit = %d, want 10000", l.DebitCents)
	}
}

func TestPaymentAndVendorPaymentLegs(t *testing.T) {
	cases := map[string]struct {
		legs          []Leg
		debit, credit string
	}{
		"payment received": {PaymentReceivedLegs(2500), SlotUndeposited, SlotAR},
		"payment deposit":  {PaymentDepositLegs(2500), SlotBank, SlotUndeposited},
		"vendor payment":   {VendorPaymentLegs(2500), SlotAP, SlotBank},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if d, ok := legFor(c.legs, c.debit); !ok || d.DebitCents != 2500 {
				t.Errorf("want debit of 2500 on %s, got %+v", c.debit, c.legs)
			}
			if cr, ok := legFor(c.legs, c.credit); !ok || cr.CreditCents != 2500 {
				t.Errorf("want credit of 2500 on %s, got %+v", c.credit, c.legs)
			}
		})
	}
}

func TestActiveLegs(t *testing.T) {
	t.Run("zero legs are dropped", func(t *testing.T) {
		got, err := activeLegs(InvoiceLegs(InvoiceAmounts{Grand: 5000}))
		if err != nil || len(got) != 2 {
			t.Fatalf("got %d legs, err %v; want AR + revenue only", len(got), err)
		}
	})
	t.Run("a zero-value document has nothing to post", func(t *testing.T) {
		got, err := activeLegs(InvoiceLegs(InvoiceAmounts{}))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %d legs, err %v; want none", len(got), err)
		}
	})
	t.Run("tax larger than the total is a user-facing data error", func(t *testing.T) {
		// Plugged revenue would be negative: 1000 grand - 1500 tax.
		_, err := activeLegs(InvoiceLegs(InvoiceAmounts{Tax: 1500, Grand: 1000}))
		if _, ok := AsUserError(err); !ok {
			t.Fatalf("want a UserError, got %v", err)
		}
	})
	t.Run("an unbalanced set is a programming error, not a user error", func(t *testing.T) {
		_, err := activeLegs([]Leg{debit(SlotAR, 100), credit(SlotSalesRevenue, 90)})
		if err == nil {
			t.Fatal("want an error")
		}
		if _, ok := AsUserError(err); ok {
			t.Fatal("an unbalanced entry is a bug, not something the user can fix")
		}
	})
}

func TestAsUserError(t *testing.T) {
	if _, ok := AsUserError(nil); ok {
		t.Error("nil is not a user error")
	}
	if _, ok := AsUserError(errors.New("boom")); ok {
		t.Error("a plain error is not a user error")
	}
	wrapped := errors.Join(errors.New("ctx"), UserError{Msg: "fix me"})
	if ue, ok := AsUserError(wrapped); !ok || ue.Msg != "fix me" {
		t.Errorf("wrapped user error not found: %v %v", ue, ok)
	}
}
