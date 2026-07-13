package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestItem_JSONKeys(t *testing.T) {
	b, _ := json.Marshal(Item{ID: "u", SKU: "SLB-1", Name: "Slab"})
	for _, k := range []string{`"id"`, `"sku"`, `"name"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
}
