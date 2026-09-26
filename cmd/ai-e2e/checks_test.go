package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluateCase(t *testing.T) {
	tests := []struct {
		name          string
		c             testCase
		res           askResult
		wantPass      bool
		wantFailParts []string // substrings expected in firstFail when wantPass is false
	}{
		{
			name:     "expect_status match short-circuits other checks",
			c:        testCase{ExpectStatus: 400},
			res:      askResult{HTTPStatus: 400},
			wantPass: true,
		},
		{
			name:          "expect_status mismatch",
			c:             testCase{ExpectStatus: 400},
			res:           askResult{HTTPStatus: 200},
			wantPass:      false,
			wantFailParts: []string{"expect_status"},
		},
		{
			name:          "unexpected non-200 status",
			c:             testCase{},
			res:           askResult{HTTPStatus: 403},
			wantPass:      false,
			wantFailParts: []string{"unexpected http status", "403"},
		},
		{
			name:          "transport error",
			c:             testCase{},
			res:           askResult{HTTPStatus: 200, TransportErr: assertErr("boom")},
			wantPass:      false,
			wantFailParts: []string{"transport error"},
		},
		{
			name:          "unexpected error event",
			c:             testCase{},
			res:           askResult{HTTPStatus: 200, ErrorPayload: map[string]any{"message": "model busy"}},
			wantPass:      false,
			wantFailParts: []string{"model busy"},
		},
		{
			name:     "expect_contains all present",
			c:        testCase{ExpectContains: []string{"Quote", "Sales → Quotes"}},
			res:      askResult{HTTPStatus: 200, Answer: "Go to Sales → Quotes to create a Quote."},
			wantPass: true,
		},
		{
			name:          "expect_contains one missing",
			c:             testCase{ExpectContains: []string{"Quote", "Invoice"}},
			res:           askResult{HTTPStatus: 200, Answer: "Go to Sales → Quotes to create a Quote."},
			wantPass:      false,
			wantFailParts: []string{"expect_contains", "Invoice"},
		},
		{
			name:     "expect_any one present",
			c:        testCase{ExpectAny: []string{"invoice", "quote"}},
			res:      askResult{HTTPStatus: 200, Answer: "Create a quote from Sales."},
			wantPass: true,
		},
		{
			name:          "expect_any none present",
			c:             testCase{ExpectAny: []string{"invoice", "purchase order"}},
			res:           askResult{HTTPStatus: 200, Answer: "Create a quote from Sales."},
			wantPass:      false,
			wantFailParts: []string{"expect_any"},
		},
		{
			name:          "expect_not_contains violated",
			c:             testCase{ExpectNotContains: []string{"/api/"}},
			res:           askResult{HTTPStatus: 200, Answer: "Call /api/tenant/crm/lead/records"},
			wantPass:      false,
			wantFailParts: []string{"expect_not_contains", "/api/"},
		},
		{
			name:     "expect_refusal satisfied",
			c:        testCase{ExpectRefusal: true},
			res:      askResult{HTTPStatus: 200, Answer: "I don't have that information."},
			wantPass: true,
		},
		{
			name:          "expect_refusal not satisfied",
			c:             testCase{ExpectRefusal: true},
			res:           askResult{HTTPStatus: 200, Answer: "The CEO likes blue."},
			wantPass:      false,
			wantFailParts: []string{"expect_refusal"},
		},
		{
			name:     "expect_refusal case-insensitive",
			c:        testCase{ExpectRefusal: true},
			res:      askResult{HTTPStatus: 200, Answer: "I DON'T HAVE THAT INFORMATION about that."},
			wantPass: true,
		},
		{
			name:          "expect_route mismatch",
			c:             testCase{ExpectRoute: "count_direct"},
			res:           askResult{HTTPStatus: 200, Route: "rag"},
			wantPass:      false,
			wantFailParts: []string{"expect_route"},
		},
		{
			name:          "expect_min_sources not met",
			c:             testCase{ExpectMinSources: 2},
			res:           askResult{HTTPStatus: 200, SourcesCount: 1},
			wantPass:      false,
			wantFailParts: []string{"expect_min_sources"},
		},
		{
			name:     "expect_min_sources met",
			c:        testCase{ExpectMinSources: 2},
			res:      askResult{HTTPStatus: 200, SourcesCount: 3},
			wantPass: true,
		},
		{
			name:     "no expectations at all always passes on 200",
			c:        testCase{},
			res:      askResult{HTTPStatus: 200, Answer: "anything"},
			wantPass: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pass, firstFail := evaluateCase(tc.c, tc.res)
			assert.Equal(t, tc.wantPass, pass)
			if tc.wantPass {
				assert.Empty(t, firstFail)
				return
			}
			for _, part := range tc.wantFailParts {
				assert.Contains(t, firstFail, part)
			}
		})
	}
}

func TestAllContain(t *testing.T) {
	missing, ok := allContain("go to sales → quotes", []string{"Sales", "Quotes"})
	assert.True(t, ok)
	assert.Empty(t, missing)

	missing, ok = allContain("go to sales → quotes", []string{"Sales", "Invoices"})
	assert.False(t, ok)
	assert.Equal(t, "Invoices", missing)
}

func TestAnyContain(t *testing.T) {
	assert.True(t, anyContain("hello world", []string{"nope", "world"}))
	assert.False(t, anyContain("hello world", []string{"nope", "missing"}))
}

func TestNoneContain(t *testing.T) {
	found, ok := noneContain("no api here", []string{"/api/", "rag_"})
	assert.True(t, ok)
	assert.Empty(t, found)

	found, ok = noneContain("call the rag_chunks table", []string{"/api/", "rag_"})
	assert.False(t, ok)
	assert.Equal(t, "rag_", found)
}

// assertErr is a minimal error for tests that only need a non-nil error.
type assertErr string

func (e assertErr) Error() string { return string(e) }
