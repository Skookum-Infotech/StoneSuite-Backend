//go:build dbtest

package chartofaccounts

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/query"
)

// allAccounts pages through Search exactly as the Tree controller does -- the
// seeded chart alone exceeds query.MaxLimit, so a single page would silently
// build the report from a prefix of it.
func allAccounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []*Account {
	t.Helper()
	page, err := Search(ctx, pool, query.Request{Limit: query.MaxLimit}, Filters{})
	require.NoError(t, err)
	out := page.Records
	for page.HasMore {
		page, err = Search(ctx, pool,
			query.Request{Limit: query.MaxLimit, Cursor: page.NextCursor}, Filters{})
		require.NoError(t, err)
		out = append(out, page.Records...)
	}
	return out
}

// categoryIDByCode resolves a seeded category code to its internal id, the
// category-placement twin of subcategoryID.
func categoryIDByCode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code int) int {
	t.Helper()
	var id int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT category_id FROM lkp_coa_category WHERE category_code=$1`, code).Scan(&id))
	return id
}

// purgeCategory hard-deletes a tenant-created category, its sub-categories, any
// accounts under either, and their audit rows. Fixture teardown only --
// production has no delete path for the taxonomy at all. TestSeedCounts pins
// the 9/17 seeded shape, so a test that appends must leave nothing behind
// whatever order the suite runs in.
func purgeCategory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code int) {
	t.Helper()
	stmts := []string{
		`DELETE FROM coa_account_history WHERE coa_account_id IN (
		     SELECT a.coa_account_id FROM coa_account a
		     LEFT JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
		     WHERE COALESCE(s.category_id, a.category_id) IN
		           (SELECT category_id FROM lkp_coa_category WHERE category_code = $1))`,
		`DELETE FROM coa_account WHERE coa_account_id IN (
		     SELECT a.coa_account_id FROM coa_account a
		     LEFT JOIN lkp_coa_subcategory s ON s.subcategory_id = a.subcategory_id
		     WHERE COALESCE(s.category_id, a.category_id) IN
		           (SELECT category_id FROM lkp_coa_category WHERE category_code = $1))`,
		`DELETE FROM lkp_coa_subcategory WHERE category_id IN
		     (SELECT category_id FROM lkp_coa_category WHERE category_code = $1)`,
		`DELETE FROM lkp_coa_category WHERE category_code = $1`,
		// The audit trail outlives its subject by design (no FK), so it has to
		// be purged by code. Without this a re-run against the same database
		// sees the previous run's create row and counts two.
		`DELETE FROM coa_taxonomy_history WHERE taxonomy_kind = 'category' AND taxonomy_code = $1`,
	}
	for _, sql := range stmts {
		_, err := pool.Exec(ctx, sql, code)
		require.NoError(t, err)
	}
}

// purgeSubCategory hard-deletes a tenant-created sub-category and everything
// under it.
func purgeSubCategory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code int) {
	t.Helper()
	stmts := []string{
		`DELETE FROM coa_account_history WHERE coa_account_id IN (
		     SELECT coa_account_id FROM coa_account WHERE subcategory_id IN
		         (SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = $1))`,
		`DELETE FROM coa_account WHERE subcategory_id IN
		     (SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = $1)`,
		`DELETE FROM lkp_coa_subcategory WHERE subcategory_code = $1`,
		`DELETE FROM coa_taxonomy_history WHERE taxonomy_kind = 'subcategory' AND taxonomy_code = $1`,
	}
	for _, sql := range stmts {
		_, err := pool.Exec(ctx, sql, code)
		require.NoError(t, err)
	}
}

// taxonomyHistoryCount counts audit rows for one taxonomy target.
func taxonomyHistoryCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, code int, action string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM coa_taxonomy_history
		WHERE taxonomy_kind = $1 AND taxonomy_code = $2 AND history_action = $3`,
		kind, code, action).Scan(&n))
	return n
}

// The nine seeded categories fill 1000-9000, so the first tenant-created one
// lands on 10000 with a full thousand-block of its own.
func TestCreateCategoryAllocatesTheNextThousandBlock(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	purgeCategory(t, ctx, pool, 10000)
	t.Cleanup(func() { purgeCategory(t, ctx, pool, 10000) })

	cat, err := CreateCategory(ctx, pool, CategoryCreateInput{
		Name: "Statistical Accounts", BSPNL: BalanceSheet, NormalBalance: NormalBalanceDebit,
	}, 1)
	require.NoError(t, err)

	assert.Equal(t, 10000, cat.Code)
	assert.Equal(t, "Statistical Accounts", cat.Name)
	assert.Equal(t, 10000, cat.RangeLow)
	assert.Equal(t, 10999, cat.RangeHigh)
	assert.Equal(t, BalanceSheet, cat.BSPNL)
	assert.Equal(t, NormalBalanceDebit, cat.NormalBalance)
	assert.Equal(t, 10, cat.SortOrder, "appended after the nine seeded categories")
	assert.Equal(t, 1, taxonomyHistoryCount(t, ctx, pool, "category", 10000, "create"))
}

func TestCreateCategoryRejectsBadInput(t *testing.T) {
	pool, ctx := testPool(t), context.Background()

	tests := []struct {
		name    string
		in      CategoryCreateInput
		wantErr string
	}{
		{"blank name", CategoryCreateInput{Name: "  ", BSPNL: BalanceSheet, NormalBalance: NormalBalanceDebit},
			"A category name is required"},
		{"unknown side", CategoryCreateInput{Name: "X", BSPNL: "BOTH", NormalBalance: NormalBalanceDebit},
			"bsPnl must be"},
		{"unknown normal balance", CategoryCreateInput{Name: "X", BSPNL: BalanceSheet, NormalBalance: "either"},
			"normalBalance must be"},
		{"over-long name", CategoryCreateInput{
			Name:  "0123456789012345678901234567890123456789012345678901234567890",
			BSPNL: BalanceSheet, NormalBalance: NormalBalanceDebit},
			"limited to 60 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CreateCategory(ctx, pool, tt.in, 1)
			require.Error(t, err)
			assert.True(t, IsClientError(err), "want ClientError, got %T", err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// 6000 Operating Expenses is the densest seeded category (6100-6500), so the
// next free hundred-block is 6600 -- and 6000-6099 stays reserved for accounts
// placed directly on the category.
func TestCreateSubCategoryAllocatesTheNextHundredBlock(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	purgeSubCategory(t, ctx, pool, 6600)
	t.Cleanup(func() { purgeSubCategory(t, ctx, pool, 6600) })

	sub, err := CreateSubCategory(ctx, pool, SubCategoryCreateInput{
		CategoryID: categoryIDByCode(t, ctx, pool, 6000), Name: "Research & Development",
	}, 1)
	require.NoError(t, err)

	assert.Equal(t, 6600, sub.Code)
	assert.Equal(t, 6600, sub.RangeLow)
	assert.Equal(t, 6699, sub.RangeHigh)
	assert.Equal(t, 6000, sub.CategoryCode)
	assert.Equal(t, 1, taxonomyHistoryCount(t, ctx, pool, "subcategory", 6600, "create"))
}

func TestCreateSubCategoryRejectsUnknownCategory(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	_, err := CreateSubCategory(ctx, pool, SubCategoryCreateInput{CategoryID: 999999, Name: "Nope"}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "Unknown category id")
}

// A seeded category is renameable -- that is the whole point of relaxing AD-1 --
// but only its name moves.
func TestRenameCategoryChangesOnlyTheName(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	id := categoryIDByCode(t, ctx, pool, 1000)
	before, _, err := Categories(ctx, pool)
	require.NoError(t, err)
	original := before[0]
	require.Equal(t, 1000, original.Code)
	t.Cleanup(func() {
		_, err := RenameCategory(ctx, pool, id, RenameInput{Name: original.Name}, 1)
		require.NoError(t, err)
	})

	got, err := RenameCategory(ctx, pool, id, RenameInput{Name: "Assets (Group)"}, 1)
	require.NoError(t, err)

	assert.Equal(t, "Assets (Group)", got.Name)
	assert.Equal(t, original.Code, got.Code, "code is immutable")
	assert.Equal(t, original.RangeLow, got.RangeLow, "range is immutable")
	assert.Equal(t, original.RangeHigh, got.RangeHigh)
	assert.Equal(t, original.NormalBalance, got.NormalBalance)
	assert.Equal(t, original.BSPNL, got.BSPNL)
	assert.GreaterOrEqual(t, taxonomyHistoryCount(t, ctx, pool, "category", 1000, "update"), 1)
}

// Renaming to the current name is a no-op: no UPDATE, and no audit row claiming
// a change that did not happen.
func TestRenameCategoryToSameNameWritesNoHistory(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	id := categoryIDByCode(t, ctx, pool, 3000)
	cats, _, err := Categories(ctx, pool)
	require.NoError(t, err)
	var current string
	for _, c := range cats {
		if c.Code == 3000 {
			current = c.Name
		}
	}
	require.NotEmpty(t, current)

	before := taxonomyHistoryCount(t, ctx, pool, "category", 3000, "update")
	_, err = RenameCategory(ctx, pool, id, RenameInput{Name: current}, 1)
	require.NoError(t, err)
	assert.Equal(t, before, taxonomyHistoryCount(t, ctx, pool, "category", 3000, "update"))
}

func TestRenameCategoryNotFound(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	_, err := RenameCategory(ctx, pool, 999999, RenameInput{Name: "Nope"}, 1)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRenameSubCategory(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	var id int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = 1200`).Scan(&id))
	_, subs, err := Categories(ctx, pool)
	require.NoError(t, err)
	var original string
	for _, s := range subs {
		if s.Code == 1200 {
			original = s.Name
		}
	}
	require.NotEmpty(t, original)
	t.Cleanup(func() {
		_, err := RenameSubCategory(ctx, pool, id, RenameInput{Name: original}, 1)
		require.NoError(t, err)
	})

	got, err := RenameSubCategory(ctx, pool, id, RenameInput{Name: "Property, Plant & Equipment"}, 1)
	require.NoError(t, err)
	assert.Equal(t, "Property, Plant & Equipment", got.Name)
	assert.Equal(t, 1200, got.Code, "code is immutable")
	assert.Equal(t, 1000, got.CategoryCode)
}

// The original complaint: an account had to go under a sub-category. Placed on
// the category directly, it takes a code from the category's reserved first
// block and reports no sub-category at all.
func TestCreateAccountDirectlyUnderACategory(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	catID := categoryIDByCode(t, ctx, pool, 3000)

	acct, err := Create(ctx, pool, testCipher(t), CreateInput{
		Name: "Equity Control", CategoryID: catID, Type: "general",
	}, 1)
	require.NoError(t, err)
	t.Cleanup(func() { purgeAccountByUUID(t, ctx, pool, acct.ID) })

	assert.Equal(t, "3000", acct.Code, "allocated from CategoryDirectRange, not a sub-category")
	assert.Nil(t, acct.SubCategoryID, "no sub-category placement")
	assert.Nil(t, acct.SubCategoryCode)
	assert.Empty(t, acct.SubCategoryName)
	assert.Equal(t, catID, acct.CategoryID)
	assert.Equal(t, 3000, acct.CategoryCode)
	assert.Equal(t, BalanceSheet, acct.BSPNL, "derived from the category, not supplied")
	assert.Equal(t, 0, acct.Depth)
}

// A sub-account of a category-placed account inherits the category placement,
// which is what fk_coa_parent_category enforces at the database level.
func TestCreateChildOfACategoryPlacedAccount(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	catID := categoryIDByCode(t, ctx, pool, 8000)

	parent, err := Create(ctx, pool, testCipher(t), CreateInput{Name: "Other Income Control", CategoryID: catID}, 1)
	require.NoError(t, err)
	t.Cleanup(func() { purgeAccountByUUID(t, ctx, pool, parent.ID) })

	child, err := Create(ctx, pool, testCipher(t), CreateInput{Name: "Scrap Sales", ParentID: parent.ID}, 1)
	require.NoError(t, err)
	t.Cleanup(func() { purgeAccountByUUID(t, ctx, pool, child.ID) })

	assert.Equal(t, parent.Code+".01", child.Code)
	assert.Equal(t, 1, child.Depth)
	assert.Nil(t, child.SubCategoryID)
	assert.Equal(t, catID, child.CategoryID)
	assert.Equal(t, ProfitAndLoss, child.BSPNL, "8000 Other Income is a P&L category")
	require.NotNil(t, child.ParentID)
	assert.Equal(t, parent.ID, *child.ParentID)
}

// 9000 System & Control is the one MIXED category: its accounts each carry
// their own side, so one placed directly on it must say which (AD-2).
func TestCreateAccountUnderMixedCategoryRequiresASide(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	catID := categoryIDByCode(t, ctx, pool, 9000)

	_, err := Create(ctx, pool, testCipher(t), CreateInput{Name: "Control", CategoryID: catID}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "want ClientError, got %T", err)
	assert.Contains(t, err.Error(), "bsPnl is required")

	acct, err := Create(ctx, pool, testCipher(t), CreateInput{
		Name: "Control", CategoryID: catID, BSPNL: ProfitAndLoss,
	}, 1)
	require.NoError(t, err)
	t.Cleanup(func() { purgeAccountByUUID(t, ctx, pool, acct.ID) })
	assert.Equal(t, ProfitAndLoss, acct.BSPNL)
}

func TestCreateAccountPlacementValidation(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	catID := categoryIDByCode(t, ctx, pool, 1000)
	subID := subcategoryID(t, ctx, pool, 1100)

	tests := []struct {
		name    string
		in      CreateInput
		wantErr string
	}{
		{"neither placement", CreateInput{Name: "X"},
			"A category or sub-category is required"},
		{"both placements", CreateInput{Name: "X", CategoryID: catID, SubCategoryID: subID},
			"either a sub-category or a category, not both"},
		{"unknown category", CreateInput{Name: "X", CategoryID: 999999},
			"Unknown category id"},
		{"unknown sub-category", CreateInput{Name: "X", SubCategoryID: 999999},
			"Unknown sub-category id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Create(ctx, pool, nil, tt.in, 1)
			require.Error(t, err)
			assert.True(t, IsClientError(err), "want ClientError, got %T", err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// A caller that echoes a placement contradicting the parent's is reporting a
// bug, not asking to move the account.
func TestCreateChildRejectsAContradictoryPlacement(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	catID := categoryIDByCode(t, ctx, pool, 8000)

	parent, err := Create(ctx, pool, testCipher(t), CreateInput{Name: "Other Income Control", CategoryID: catID}, 1)
	require.NoError(t, err)
	t.Cleanup(func() { purgeAccountByUUID(t, ctx, pool, parent.ID) })

	_, err = Create(ctx, pool, testCipher(t), CreateInput{
		Name: "Wrong", ParentID: parent.ID, SubCategoryID: subcategoryID(t, ctx, pool, 1100),
	}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "parent's category (8000)")
}

// An account under a tenant-created sub-category must derive its side from the
// parent category -- the case the old hardcoded sub-category map could not
// answer at all.
func TestCreateAccountUnderATenantCreatedSubCategory(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	purgeSubCategory(t, ctx, pool, 6600)
	t.Cleanup(func() { purgeSubCategory(t, ctx, pool, 6600) })

	sub, err := CreateSubCategory(ctx, pool, SubCategoryCreateInput{
		CategoryID: categoryIDByCode(t, ctx, pool, 6000), Name: "Research & Development",
	}, 1)
	require.NoError(t, err)

	acct, err := Create(ctx, pool, testCipher(t), CreateInput{Name: "R&D Salaries", SubCategoryID: sub.ID}, 1)
	require.NoError(t, err)

	assert.Equal(t, "6600", acct.Code)
	assert.Equal(t, ProfitAndLoss, acct.BSPNL, "inherited from 6000 Operating Expenses")
	require.NotNil(t, acct.SubCategoryCode)
	assert.Equal(t, 6600, *acct.SubCategoryCode)
	assert.Equal(t, 6000, acct.CategoryCode)
}

// The report must show a tenant-created category even before anything is filed
// under it, and must file a category-placed account on the category node rather
// than inside one of its sub-categories.
func TestTreeReportsTenantCategoryAndDirectAccounts(t *testing.T) {
	pool, ctx := testPool(t), context.Background()
	purgeCategory(t, ctx, pool, 10000)
	t.Cleanup(func() { purgeCategory(t, ctx, pool, 10000) })

	cat, err := CreateCategory(ctx, pool, CategoryCreateInput{
		Name: "Statistical Accounts", BSPNL: ProfitAndLoss, NormalBalance: NormalBalanceCredit,
	}, 1)
	require.NoError(t, err)

	cats, subs, err := Categories(ctx, pool)
	require.NoError(t, err)
	sections := BuildTree(cats, subs, allAccounts(t, ctx, pool), TreeOptions{})

	found := findTreeCategory(sections, cat.Code)
	require.NotNil(t, found, "an empty tenant category must still be reported")
	assert.Empty(t, found.SubCategories)
	assert.Empty(t, found.Accounts)

	acct, err := Create(ctx, pool, testCipher(t), CreateInput{Name: "Headcount", CategoryID: cat.ID}, 1)
	require.NoError(t, err)

	cats, subs, err = Categories(ctx, pool)
	require.NoError(t, err)
	sections = BuildTree(cats, subs, allAccounts(t, ctx, pool), TreeOptions{})

	found = findTreeCategory(sections, cat.Code)
	require.NotNil(t, found)
	require.Len(t, found.Accounts, 1)
	assert.Equal(t, acct.Code, found.Accounts[0].Code)
}

// findTreeCategory locates a category node anywhere in the report.
func findTreeCategory(sections []TreeSection, code int) *TreeCategory {
	for _, sec := range sections {
		for _, c := range sec.Categories {
			if c.Code == code {
				return c
			}
		}
	}
	return nil
}
