package invoice

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvoice_JSONKeys(t *testing.T) {
	b, err := json.Marshal(Invoice{ID: "i1", Number: "INVC-000001", StatusName: "Draft"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, k := range []string{`"id"`, `"invoiceNumber"`, `"status"`, `"customer"`, `"items"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
}

func TestLine_JSONKeys(t *testing.T) {
	b, err := json.Marshal(Line{ID: "l1", LineNumber: 1, SKU: "SLB-1", ItemName: "Slab"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, k := range []string{`"id"`, `"lineNumber"`, `"sku"`, `"itemName"`, `"lineTotal"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
}

func TestCreateInvoiceInput_JSONKeys(t *testing.T) {
	in := CreateInvoiceInput{CustomerUUID: "c1", Items: []InvoiceLineInput{{LineNumber: 1, Quantity: 1, UnitPrice: 1}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, k := range []string{`"customerUuid"`, `"items"`, `"shipSameAsBilling"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
}
