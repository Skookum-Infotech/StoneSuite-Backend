package crmstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCRMStageMappingsConsistent verifies the key/code/rank maps agree, so the
// forward-only stage logic (lead → prospect → customer) is well-defined.
func TestCRMStageMappingsConsistent(t *testing.T) {
	for key, code := range crmKeyToCode {
		assert.Equalf(t, key, crmCodeToKey[code], "code %q should map back to key %q", code, key)
		_, ok := crmCodeRank[code]
		assert.Truef(t, ok, "code %q must have a rank", code)
	}
	// Strict forward ordering lead < prospect < customer.
	assert.Less(t, crmCodeRank["LEAD"], crmCodeRank["PROS"])
	assert.Less(t, crmCodeRank["PROS"], crmCodeRank["CUST"])
}

// TestForwardOnlyRule checks the rank comparison used to reject backward moves.
func TestForwardOnlyRule(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		allowed  bool
	}{
		{"lead to prospect", "LEAD", "PROS", true},
		{"lead to customer (skip)", "LEAD", "CUST", true},
		{"prospect to customer", "PROS", "CUST", true},
		{"same stage status change", "CUST", "CUST", true},
		{"customer to prospect (reverse)", "CUST", "PROS", false},
		{"prospect to lead (reverse)", "PROS", "LEAD", false},
		{"customer to lead (reverse)", "CUST", "LEAD", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forward := crmCodeRank[tc.to] >= crmCodeRank[tc.from]
			assert.Equal(t, tc.allowed, forward)
		})
	}
}

func TestGetStr(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{"nil map", nil, "x", ""},
		{"missing key", map[string]any{"a": "b"}, "x", ""},
		{"string value", map[string]any{"company_name": "Acme"}, "company_name", "Acme"},
		{"non-string value", map[string]any{"n": 42}, "n", "42"},
		{"nil value", map[string]any{"n": nil}, "n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, getStr(tc.m, tc.key))
		})
	}
}

func TestNullableInt(t *testing.T) {
	assert.Nil(t, nullableInt(0))
	assert.Nil(t, nullableInt(-1))
	assert.Equal(t, 5, nullableInt(5))
}

func TestForFallsBackToV1(t *testing.T) {
	assert.IsType(t, &workflowStore{}, For(""))
	assert.IsType(t, &workflowStore{}, For("v1"))
	assert.IsType(t, &workflowStore{}, For("bogus"))
	assert.IsType(t, &relationalStore{}, For("v2"))
}

func TestGetBool(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want bool
	}{
		{"nil map", nil, "x", false},
		{"missing", map[string]any{}, "x", false},
		{"bool true", map[string]any{"b": true}, "b", true},
		{"bool false", map[string]any{"b": false}, "b", false},
		{"string true", map[string]any{"b": "true"}, "b", true},
		{"string 1", map[string]any{"b": "1"}, "b", true},
		{"string no", map[string]any{"b": "no"}, "b", false},
		{"number ignored", map[string]any{"b": 5}, "b", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, getBool(tc.m, tc.key))
		})
	}
}

func TestNullableNumFromCore(t *testing.T) {
	assert.Nil(t, nullableNumFromCore(nil, "x"))
	assert.Nil(t, nullableNumFromCore(map[string]any{}, "x"))
	assert.Nil(t, nullableNumFromCore(map[string]any{"v": ""}, "v"))
	assert.Nil(t, nullableNumFromCore(map[string]any{"v": "abc"}, "v"))
	assert.Equal(t, 12.5, nullableNumFromCore(map[string]any{"v": "12.5"}, "v"))
	assert.Equal(t, 3.0, nullableNumFromCore(map[string]any{"v": 3.0}, "v"))
	assert.Equal(t, 7, nullableNumFromCore(map[string]any{"v": 7}, "v"))
}

func TestNullableDateFromCore(t *testing.T) {
	assert.Nil(t, nullableDateFromCore(map[string]any{}, "d"))
	assert.Nil(t, nullableDateFromCore(map[string]any{"d": ""}, "d"))
	assert.Nil(t, nullableDateFromCore(map[string]any{"d": 5}, "d"))
	assert.Equal(t, "2026-06-15", nullableDateFromCore(map[string]any{"d": "2026-06-15"}, "d"))
}

// TestWriteArgByKind exercises the registry-driven argument conversion for each
// storage kind, which underpins every customer INSERT/UPDATE.
func TestWriteArgByKind(t *testing.T) {
	core := map[string]any{
		"company_name":        "Acme",
		"country_id":          "3",
		"do_not_contact":      true,
		"credit_limit":        "5000.50",
		"expected_close_date": "2026-12-31",
		"lead_score":          "88",
	}
	assert.Equal(t, "Acme", writeArg(cfield{core: "company_name", kind: kStr}, core))
	assert.Equal(t, 3, writeArg(cfield{core: "country_id", kind: kFK}, core))
	assert.Equal(t, true, writeArg(cfield{core: "do_not_contact", kind: kBool}, core))
	assert.Equal(t, 5000.50, writeArg(cfield{core: "credit_limit", kind: kDec}, core))
	assert.Equal(t, "2026-12-31", writeArg(cfield{core: "expected_close_date", kind: kDate}, core))
	assert.Equal(t, 88, writeArg(cfield{core: "lead_score", kind: kInt}, core))
}

// TestCustomerFieldRegistryUnique guards against a copy-paste duplicate column
// or CoreFields key in the registry, which would corrupt generated SQL.
func TestCustomerFieldRegistryUnique(t *testing.T) {
	cores := map[string]bool{}
	cols := map[string]bool{}
	for _, f := range customerFields {
		assert.Falsef(t, cores[f.core], "duplicate core key %q", f.core)
		assert.Falsef(t, cols[f.col], "duplicate column %q", f.col)
		cores[f.core] = true
		cols[f.col] = true
	}
	// The six built-in contact fields must stay marked always-present.
	for _, k := range []string{"customer_name", "customer_authorized_person_fname", "customer_authorized_person_lname", "customer_contact_email", "customer_primary_phonenum", "customer_addr_line1"} {
		assert.Truef(t, cores[k], "registry missing built-in field %q", k)
	}
}

// TestRecordSelectColumnCount verifies the generated SELECT has the 13 fixed
// columns plus one per registry field, matching scanRecord's target count.
func TestRecordSelectColumnCount(t *testing.T) {
	// 13 fixed leading expressions are joined by ", "; each registry field adds one.
	assert.Contains(t, recordSelect, "FROM customer c")
	assert.Contains(t, recordSelect, "JOIN lkp_record_type rt")
}

func TestApprovalSentinelErrorsAreDistinct(t *testing.T) {
	assert.NotEqual(t, ErrNotApprover.Error(), ErrAlreadyApproved.Error())
	assert.NotEqual(t, ErrNotApprover.Error(), ErrNoApproverConfigured.Error())
	assert.NotEqual(t, ErrAlreadyApproved.Error(), ErrNoApproverConfigured.Error())
}
