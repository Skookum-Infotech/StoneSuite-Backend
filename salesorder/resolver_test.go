package salesorder

import (
	"testing"

	"stonesuite-backend/query"
)

// TestResolverWhitelist verifies the filter resolver only resolves whitelisted
// keys (unknown keys must fall through so the engine returns 400, never raw
// SQL), and that the AD-8 payment_due_date field is filterable as a date.
func TestResolverWhitelist(t *testing.T) {
	r := resolver{}

	t.Run("payment_due_date is a filterable date (AD-8)", func(t *testing.T) {
		expr, dt, ok := r.Resolve("payment_due_date")
		if !ok {
			t.Fatal("expected payment_due_date to resolve")
		}
		if expr != "so.sales_order_payment_due_date" {
			t.Errorf("unexpected expr %q", expr)
		}
		if dt != query.TypeDate {
			t.Errorf("expected TypeDate, got %v", dt)
		}
	})

	t.Run("unknown key does not resolve", func(t *testing.T) {
		if _, _, ok := r.Resolve("sales_order_grand_total; DROP TABLE sales_order"); ok {
			t.Error("unknown/injection key must not resolve")
		}
	})

	t.Run("valid custom-field key resolves, bad one does not", func(t *testing.T) {
		if _, _, ok := r.Resolve("cf:install_required"); !ok {
			t.Error("expected valid cf: key to resolve")
		}
		if _, _, ok := r.Resolve("cf:BadKey"); ok {
			t.Error("cf: key failing the regex must not resolve")
		}
	})
}
