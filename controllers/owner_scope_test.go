package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/authz"
)

func TestOwnerEmployeeFilter_ScopeAllNeedsNoLookup(t *testing.T) {
	owner, ok := ownerEmployeeFilter(context.Background(), nil, authz.ScopeAll, "ident")
	assert.True(t, ok)
	assert.Nil(t, owner)
}

func TestOwnerEmployeeAllows_ScopeAll(t *testing.T) {
	one := 1
	assert.True(t, ownerEmployeeAllows(context.Background(), nil, authz.ScopeAll, "ident", &one))
	assert.True(t, ownerEmployeeAllows(context.Background(), nil, authz.ScopeAll, "ident", nil))
}

func TestSafeReturnTo_RejectsControlChars(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"/dashboard", true},
		{"//evil.com", false},
		{"/\t/evil.com", false},
		{"/\n/evil.com", false},
		{"/a\b", false},
		{"https://evil.com", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) { assert.Equal(t, c.want, safeReturnTo(c.in)) })
	}
}
