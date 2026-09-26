package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvePlaceholders(t *testing.T) {
	ph := map[string]string{
		placeholderLeadNumber:    "LEAD-0042",
		placeholderLeadCity:      "Austin",
		placeholderCountCustomer: "17",
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no placeholders", in: "how many leads do I have", want: "how many leads do I have"},
		{name: "single placeholder", in: "Which city is lead {{lead.number}} in?", want: "Which city is lead LEAD-0042 in?"},
		{name: "multiple placeholders", in: "{{lead.city}} has {{counts.customer}} customers", want: "Austin has 17 customers"},
		{name: "unresolved token left as-is", in: "{{lead.name}} lives in {{lead.city}}", want: "{{lead.name}} lives in Austin"},
		{name: "repeated token replaced everywhere", in: "{{lead.city}} {{lead.city}}", want: "Austin Austin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolvePlaceholders(tc.in, ph))
		})
	}
}

func TestResolveCase(t *testing.T) {
	ph := map[string]string{placeholderCountCustomer: "17"}
	c := testCase{
		Question:          "how many customers",
		ExpectContains:    []string{"{{counts.customer}}"},
		ExpectAny:         []string{"{{counts.customer}} customers"},
		ExpectNotContains: []string{"You have {{counts.customer}} customers"},
	}
	got := resolveCase(c, ph)
	assert.Equal(t, "17", got.ExpectContains[0])
	assert.Equal(t, "17 customers", got.ExpectAny[0])
	assert.Equal(t, "You have 17 customers", got.ExpectNotContains[0])
}

func TestResolveAllNilSlice(t *testing.T) {
	assert.Nil(t, resolveAll(nil, map[string]string{}))
}
