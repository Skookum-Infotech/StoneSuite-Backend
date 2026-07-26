//go:build dbtest

package chartofaccounts

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/query"
)

// TestSearchKeysetPaginationPage2 is the regression for resolver.go's
// alias-qualified sort expression: the read query LEFT JOINs coa_account
// twice (as a and parent p), so an unqualified sort column serves page 1
// correctly and 500s on page 2 with "column reference is ambiguous" once
// query.Build starts emitting that expression inside the keyset WHERE
// predicate too.
func TestSearchKeysetPaginationPage2(t *testing.T) {
	pool, ctx := testPool(t), context.Background()

	req1 := query.Request{Sort: []query.SortKey{{Field: "code", Dir: query.DirAsc}}, Limit: 5}
	page1, err := Search(ctx, pool, req1, Filters{})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)
	require.Len(t, page1.Records, 5)

	req2 := query.Request{
		Sort: []query.SortKey{{Field: "code", Dir: query.DirAsc}}, Limit: 5, Cursor: page1.NextCursor,
	}
	page2, err := Search(ctx, pool, req2, Filters{})
	require.NoError(t, err, `regression: this used to fail with "column reference is ambiguous"`)
	require.Len(t, page2.Records, 5)

	seen := make(map[string]bool, len(page1.Records))
	for _, a := range page1.Records {
		seen[a.ID] = true
	}
	for _, a := range page2.Records {
		assert.False(t, seen[a.ID], "page 2 must not repeat a page-1 record")
	}
	assert.True(t, page1.Records[len(page1.Records)-1].Code < page2.Records[0].Code,
		"page 2 must continue strictly after page 1 in code order, with no gap or overlap")
}

// TestBankAccountNumberEncryptedAtRestAndMasked covers AD-10 end to end: the
// stored JSONB value is never the plaintext, the Go read path never returns
// the full number (only the last-4 hint), and the audit trail carries neither
// the plaintext nor the ciphertext -- only the fact that "attributes" changed.
func TestBankAccountNumberEncryptedAtRestAndMasked(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	const plaintext = "1234567890124821"
	acct, err := Create(ctx, pool, cipher, CreateInput{
		Name: "T17 Bank", SubCategoryID: subID, Type: "bank",
		Attributes: map[string]any{"bankName": "HDFC", BankAccountNumberKey: plaintext},
	}, 1)
	require.NoError(t, err)

	assert.NotContains(t, acct.Attributes, BankAccountNumberKey)
	assert.Equal(t, "4821", acct.Attributes[accountNumberLast4Key])

	got, err := Get(ctx, pool, acct.ID)
	require.NoError(t, err)
	assert.NotContains(t, got.Attributes, BankAccountNumberKey)

	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT coa_account_attributes->>'accountNumber' FROM coa_account WHERE coa_account_uuid = $1`,
		acct.ID).Scan(&stored))
	assert.NotEqual(t, plaintext, stored)
	assert.NotContains(t, stored, plaintext)

	rows, err := pool.Query(ctx, `
		SELECT history_old_value, history_new_value FROM coa_account_history
		WHERE coa_account_id = (SELECT coa_account_id FROM coa_account WHERE coa_account_uuid = $1)`,
		acct.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var oldV, newV string
		require.NoError(t, rows.Scan(&oldV, &newV))
		assert.NotContains(t, oldV, plaintext)
		assert.NotContains(t, newV, plaintext)
		assert.NotContains(t, oldV, stored)
		assert.NotContains(t, newV, stored)
	}
	require.NoError(t, rows.Err())
}

// TestTypeChangeCannotStrandAttributes is the AD-9/AD-10 breach fixed in
// 9506209: changing type away from "bank" with no attributes in the same
// request must be rejected outright, rather than silently stranding an
// encrypted account number under a type that disallows it. The documented
// escape hatch -- an explicit "attributes": {} -- must still work.
func TestTypeChangeCannotStrandAttributes(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	acct, err := Create(ctx, pool, cipher, CreateInput{
		Name: "T17 Type Change", SubCategoryID: subID, Type: "bank",
		Attributes: map[string]any{"bankName": "HDFC", BankAccountNumberKey: "1234567890124821"},
	}, 1)
	require.NoError(t, err)

	general := "general"
	_, err = Update(ctx, pool, cipher, acct.ID, UpdateInput{Type: &general}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "attributes")

	updated, err := Update(ctx, pool, cipher, acct.ID, UpdateInput{
		Type: &general, Attributes: map[string]any{},
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, "general", updated.Type)
	assert.NotContains(t, updated.Attributes, BankAccountNumberKey)
	assert.NotContains(t, updated.Attributes, accountNumberLast4Key)
}

// TestUpdateUnknownAccountTypeIsClientError: before this was checked up
// front, an unknown type reached the database and violated chk_coa_type
// (SQLSTATE 23514), which isUniqueViolation does not catch -- a 500 for
// plainly invalid input. It must now fail before ever touching the DB.
func TestUpdateUnknownAccountTypeIsClientError(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	acct, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Bad Type", SubCategoryID: subID}, 1)
	require.NoError(t, err)

	bad := "not-a-real-type"
	_, err = Update(ctx, pool, cipher, acct.ID, UpdateInput{Type: &bad}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err),
		"an unknown account type must be a 400 ClientError, not a DB constraint violation surfaced as 500: %v", err)

	var pgErr *pgconn.PgError
	assert.False(t, errors.As(err, &pgErr), "must be rejected before ever reaching the database")
}

// TestMalformedUUIDIsClientErrorAcrossMutations covers the other reachable
// entry points beyond Get/History/Create's parent lookup (already covered
// in-process by store_uuidguard_test.go): Update, SoftDelete, and BulkUpdate,
// whose array-of-client-strings input is the most exposed surface.
func TestMalformedUUIDIsClientErrorAcrossMutations(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	const bad = "not-a-uuid"

	t.Run("Update", func(t *testing.T) {
		_, err := Update(ctx, pool, cipher, bad, UpdateInput{}, 1)
		require.Error(t, err)
		assert.True(t, IsClientError(err))
	})
	t.Run("SoftDelete", func(t *testing.T) {
		err := SoftDelete(ctx, pool, bad, 1)
		require.Error(t, err)
		assert.True(t, IsClientError(err))
	})
	t.Run("BulkUpdate", func(t *testing.T) {
		_, err := BulkUpdate(ctx, pool, BulkInput{UUIDs: []string{bad}, IsActive: boolPtr(true)}, 1)
		require.Error(t, err)
		assert.True(t, IsClientError(err))
	})
}

// TestCodeReuseAfterSoftDelete: uq_coa_account_code_live is partial on
// deleted_at IS NULL, so soft-deleting an account frees its code for reuse --
// and takenCodes, which drives the numbering allocator, must agree.
func TestCodeReuseAfterSoftDelete(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	cipher := testCipher(t)
	subID := subcategoryID(t, ctx, pool, 1100)

	first, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Reuse A", SubCategoryID: subID}, 1)
	require.NoError(t, err)
	freedCode := first.Code

	require.NoError(t, SoftDelete(ctx, pool, first.ID, 1))

	taken, err := takenCodes(ctx, pool)
	require.NoError(t, err)
	assert.NotContains(t, taken, freedCode, "takenCodes must free a soft-deleted account's code")

	second, err := Create(ctx, pool, cipher, CreateInput{Name: "T17 Reuse B", SubCategoryID: subID}, 1)
	require.NoError(t, err)
	assert.Equal(t, freedCode, second.Code, "a live create must be able to reuse the freed code")
	assert.NotEqual(t, first.ID, second.ID, "the reused code must belong to a brand new row")
}
