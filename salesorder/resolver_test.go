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

	t.Run("status_code and customer_uuid resolve to the joined aliases", func(t *testing.T) {
		for key, want := range map[string]string{
			"status_code":   "rs.record_status_code",
			"customer_uuid": "c.customer_uuid::text",
		} {
			expr, dt, ok := r.Resolve(key)
			if !ok {
				t.Fatalf("expected %s to resolve", key)
			}
			if expr != want {
				t.Errorf("%s: unexpected expr %q", key, expr)
			}
			if dt != query.TypeString {
				t.Errorf("%s: expected TypeString, got %v", key, dt)
			}
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
