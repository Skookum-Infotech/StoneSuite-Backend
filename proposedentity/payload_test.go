package proposedentity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/proposedaction"
)

const testRef = "0b0c1c52-3c5e-4c6e-9a53-1d2f6a7b8c9d:pe_abc123"

func mustJSON(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestCustomerValidate(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"sourceRef": testRef, "name": "Acme Stone", "contactEmail": "buyer@acme.example"}
	}
	tests := []struct {
		name    string
		mutate  func(m map[string]any)
		wantErr string
	}{
		{"ok", func(m map[string]any) {}, ""},
		{"ok with optional", func(m map[string]any) { m["taxId"] = "12-3456789"; m["address"] = "1 Main St" }, ""},
		{"unknown field", func(m map[string]any) { m["extra"] = 1 }, "not a valid object"},
		{"missing name", func(m map[string]any) { m["name"] = " " }, "name is required"},
		{"oversize name", func(m map[string]any) { m["name"] = strings.Repeat("a", maxCustomerName+1) }, "name is too long"},
		{"control char", func(m map[string]any) { m["name"] = "Ac\x00me" }, "invalid characters"},
		{"newline inside", func(m map[string]any) { m["name"] = "Ac\nme" }, "invalid characters"},
		{"bidi", func(m map[string]any) { m["name"] = "Ac\u202eme" }, "invalid characters"},
		{"bad email", func(m map[string]any) { m["contactEmail"] = "not-an-email" }, "valid email"},
		{"display name email", func(m map[string]any) { m["contactEmail"] = "Bob <b@x.example>" }, "valid email"},
		{"missing email", func(m map[string]any) { m["contactEmail"] = "" }, "contactEmail is required"},
		{"masked placeholder", func(m map[string]any) { m["name"] = "Acme [masked]" }, "sensitive"},
		{"card number", func(m map[string]any) { m["address"] = "pay 4111 1111 1111 1111" }, "sensitive"},
		{"long tax id", func(m map[string]any) { m["taxId"] = strings.Repeat("1", maxTaxID+1) }, "taxId is too long"},
		{"long address", func(m map[string]any) { m["address"] = strings.Repeat("a", maxAddress+1) }, "address is too long"},
		{"missing sourceRef", func(m map[string]any) { delete(m, "sourceRef") }, "sourceRef is required"},
		{"bad sourceRef", func(m map[string]any) { m["sourceRef"] = "nope" }, "sourceRef is malformed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			err := customerExecutor{}.Validate(context.Background(), mustJSON(t, m))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			var ve *proposedaction.ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Contains(t, ve.Message, tc.wantErr)
			assert.NotContains(t, ve.Message, "Acme", "errors must not echo payload values")
		})
	}
}

func TestValidate_TrailingDataAndGarbage(t *testing.T) {
	for _, raw := range []string{`{"sourceRef":"x"} {}`, `[]`, `"s"`, `null`, `{`} {
		err := vendorExecutor{}.Validate(context.Background(), json.RawMessage(raw))
		assert.True(t, errors.Is(err, proposedaction.ErrInvalidPayload), raw)
	}
}

func TestVendorValidate(t *testing.T) {
	ok := map[string]any{"sourceRef": testRef, "name": "Granite Co"}
	require.NoError(t, vendorExecutor{}.Validate(context.Background(), mustJSON(t, ok)))
	ok["email"] = "bad"
	assert.Error(t, vendorExecutor{}.Validate(context.Background(), mustJSON(t, ok)))
	ok["email"] = "sales@granite.example"
	ok["bogus"] = true
	assert.Error(t, vendorExecutor{}.Validate(context.Background(), mustJSON(t, ok)))
	delete(ok, "bogus")
	ok["name"] = strings.Repeat("a", maxVendorName+1)
	assert.Error(t, vendorExecutor{}.Validate(context.Background(), mustJSON(t, ok)))
}

func TestItemValidate(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"sourceRef": testRef, "sku": "SLAB-1", "name": "Slab", "unitId": 1, "unitPriceCents": 100, "purchaseUnitPriceCents": 0}
	}
	tests := []struct {
		name    string
		mutate  func(m map[string]any)
		wantErr bool
	}{
		{"ok", func(m map[string]any) {}, false},
		{"no sku", func(m map[string]any) { m["sku"] = "" }, true},
		{"long name", func(m map[string]any) { m["name"] = strings.Repeat("a", maxItemName+1) }, true},
		{"long sku", func(m map[string]any) { m["sku"] = strings.Repeat("a", maxSKU+1) }, true},
		{"long description", func(m map[string]any) { m["description"] = strings.Repeat("a", maxDescription+1) }, true},
		{"long uom", func(m map[string]any) { m["docUoM"] = strings.Repeat("a", maxDocUoM+1) }, true},
		{"zero unit", func(m map[string]any) { m["unitId"] = 0 }, true},
		{"negative price", func(m map[string]any) { m["unitPriceCents"] = -1 }, true},
		{"negative purchase", func(m map[string]any) { m["purchaseUnitPriceCents"] = -1 }, true},
		{"huge price", func(m map[string]any) { m["unitPriceCents"] = maxPriceCents + 1 }, true},
		{"string price", func(m map[string]any) { m["unitPriceCents"] = "1" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			err := itemExecutor{}.Validate(context.Background(), mustJSON(t, m))
			if tc.wantErr {
				assert.ErrorIs(t, err, proposedaction.ErrInvalidPayload)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"  a \t b\n c ", 0, "a b c"},
		{"a\u202eb\x00c", 0, "abc"},
		{"abcdef", 3, "abc"},
		{"héllo", 2, "hé"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, cleanText(tc.in, tc.max))
	}
}

func TestPreviews(t *testing.T) {
	ctx := context.Background()
	cp, err := customerExecutor{}.Preview(ctx, mustJSON(t, map[string]any{
		"sourceRef": testRef, "name": "Acme", "contactEmail": "a@acme.example", "taxId": "99", "address": "1 Main"}))
	require.NoError(t, err)
	assert.Equal(t, "Create customer 'Acme'", cp.Title)
	labels := map[string]string{}
	for _, f := range cp.Fields {
		labels[f.Label] = f.Value
	}
	assert.Equal(t, "99", labels["Tax ID"])
	assert.Equal(t, "1 Main", labels["Address "+addressNotSaved])

	vp, err := vendorExecutor{}.Preview(ctx, mustJSON(t, map[string]any{"sourceRef": testRef, "name": "Granite"}))
	require.NoError(t, err)
	assert.Equal(t, "Create vendor 'Granite'", vp.Title)

	ip, err := itemExecutor{}.Preview(ctx, mustJSON(t, map[string]any{
		"sourceRef": testRef, "sku": "S1", "name": "Slab", "unitId": 2, "unitCode": "EA", "docUoM": "xyz",
		"unitPriceCents": 12345, "purchaseUnitPriceCents": 5}))
	require.NoError(t, err)
	assert.Equal(t, "Create item 'Slab'", ip.Title)
	got := map[string]string{}
	for _, f := range ip.Fields {
		got[f.Label] = f.Value
	}
	assert.Equal(t, "EA (document said 'xyz')", got["Unit"])
	assert.Equal(t, "$123.45", got["Selling price"])
	assert.Equal(t, "$0.05", got["Purchase price"])
}

func TestUnitLabel(t *testing.T) {
	tests := []struct {
		p    ItemPayload
		want string
	}{
		{ItemPayload{UnitID: 1, UnitCode: "EA"}, "EA"},
		{ItemPayload{UnitID: 1, UnitCode: "SQFT", DocUoM: "sf"}, "SQFT"},
		{ItemPayload{UnitID: 1, UnitCode: "EA", DocUoM: "weird"}, "EA (document said 'weird')"},
		{ItemPayload{UnitID: 7}, "unit #7"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, unitLabel(tc.p))
	}
}

func TestInScopeAlways(t *testing.T) {
	for _, e := range []proposedaction.Executor{customerExecutor{}, vendorExecutor{}, itemExecutor{}} {
		ok, err := e.InScope(context.Background(), nil, "", proposedaction.Caller{}, proposedaction.KindCreate, nil)
		require.NoError(t, err)
		assert.True(t, ok)
	}
}

func TestRegister(t *testing.T) {
	reg := proposedaction.NewRegistry()
	require.NoError(t, Register(reg))
	for _, res := range []string{"customer", "vendor", "inventory_item"} {
		_, ok := reg.Lookup(res, proposedaction.KindCreate)
		assert.True(t, ok, res)
	}
	assert.Error(t, Register(reg), "second registration is a duplicate")
}

// TestCheckIdentAllowsLuhnShapedSKU guards the regression where a barcode-style
// SKU that happens to pass the Luhn check was rejected as a card number.
func TestCheckIdentAllowsLuhnShapedSKU(t *testing.T) {
	const luhnShaped = "4111111111111111"
	assert.NoError(t, checkIdent("sku", luhnShaped, maxSKU, true))
	assert.Error(t, checkText("name", luhnShaped, maxItemName, true))
}
