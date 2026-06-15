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
