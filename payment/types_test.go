package payment

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPayment_JSONShape(t *testing.T) {
	p := Payment{
		ID: "abc", Number: "PYMT-000001", StatusCode: "PEND", StatusName: "Pending",
		Customer: CustomerRef{ID: "cust-1", Name: "Acme"},
		OwnerUserID: "user-should-not-serialize",
		MethodID: 1, MethodName: "Check",
		Amount: 100, AppliedTotal: 40, UnappliedAmount: 60,
		PaymentDate: time.Now(), CustomFields: map[string]any{}, Applications: []Application{},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "paymentNumber", "statusCode", "status", "customer", "methodId", "method", "amount", "appliedTotal", "unappliedAmount", "applications"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected JSON key %q, got keys %v", key, m)
		}
	}
	if _, ok := m["OwnerUserID"]; ok {
		t.Error("OwnerUserID must not serialize (json:\"-\")")
	}
}
