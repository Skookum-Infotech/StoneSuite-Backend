package chartofaccounts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoreEntryPointsRejectMalformedUUID proves the guards short-circuit
// BEFORE any query is issued: coa_account_uuid is a real uuid column, so a
// garbage string reaches Postgres as SQLSTATE 22P02 ("invalid input syntax for
// type uuid"), which is neither pgx.ErrNoRows nor a unique violation and so
// used to propagate all the way out as a 500. Passing a nil pool is the proof
// -- if the guard ever stops short-circuiting, these panic instead of
// returning a ClientError.
func TestStoreEntryPointsRejectMalformedUUID(t *testing.T) {
	ctx := context.Background()
	malformed := []string{"", "not-a-uuid", "{" + uuidA + "}", "' OR 1=1 --"}

	for _, bad := range malformed {
		t.Run("Get "+bad, func(t *testing.T) {
			_, err := Get(ctx, nil, bad)
			require.Error(t, err)
			assert.True(t, IsClientError(err), "want a 400 ClientError, got %v", err)
		})
		t.Run("History "+bad, func(t *testing.T) {
			_, err := History(ctx, nil, bad, 50)
			require.Error(t, err)
			assert.True(t, IsClientError(err), "want a 400 ClientError, got %v", err)
		})
	}
}

// TestResolveTargetRejectsMalformedParentID covers the third entry point: a
// create naming a malformed parent must be a 400, not a 500. The empty string
// is excluded deliberately -- it means "top-level account", not a bad id.
func TestResolveTargetRejectsMalformedParentID(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"not-a-uuid", "{" + uuidA + "}", "' OR 1=1 --"} {
		t.Run(bad, func(t *testing.T) {
			_, err := resolveTarget(ctx, nil, CreateInput{ParentID: bad})
			require.Error(t, err)
			assert.True(t, IsClientError(err), "want a 400 ClientError, got %v", err)
		})
	}
}
