package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// TestRecordInScope_AllShortCircuits proves that ScopeAll is resolved without
// any database access — it must return true before touching the pool. Passing a
// nil pool guarantees the test fails loudly (panic) if that contract regresses
// and an "all"-scoped caller is ever made to do an ownership lookup.
func TestRecordInScope_AllShortCircuits(t *testing.T) {
	require.NotPanics(t, func() {
		allowed, err := recordInScope(context.Background(), nil, authz.ScopeAll, "any-identity", "some-owner", "some-team")
		assert.NoError(t, err)
		assert.True(t, allowed, "scope=all must always allow")
	})
}
