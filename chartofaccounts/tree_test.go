package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ptr is the fixtures' shorthand for the nullable placement columns: an account
// carries a sub-category id or a category id, never both.
func ptr[T any](v T) *T { return &v }

func treeFixture() ([]Category, []SubCategory, []*Account) {
	cats := []Category{
		{ID: 1, Code: 1000, Name: "Assets", NormalBalance: "debit", BSPNL: BalanceSheet, SortOrder: 1},
		{ID: 4, Code: 4000, Name: "Revenue", NormalBalance: "credit", BSPNL: ProfitAndLoss, SortOrder: 4},
	}
	subs := []SubCategory{
		{ID: 1, CategoryID: 1, CategoryCode: 1000, Code: 1100, Name: "Current Assets", SortOrder: 1},
		{ID: 2, CategoryID: 1, CategoryCode: 1000, Code: 1200, Name: "Fixed Assets", SortOrder: 2},
		{ID: 7, CategoryID: 4, CategoryCode: 4000, Code: 4100, Name: "Sales", SortOrder: 1},
	}
	parent := "uuid-1103"
	accts := []*Account{
		{ID: "uuid-1103", Code: "1103", Name: "Bank Account - Operating", SubCategoryID: ptr(1),
			SubCategoryCode: ptr(1100), CategoryID: 1, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1103-01", Code: "1103.01", Name: "HDFC USA", SubCategoryID: ptr(1),
			SubCategoryCode: ptr(1100), CategoryID: 1, CategoryCode: 1000, BSPNL: "BS", Depth: 1,
			ParentID: &parent, IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1101", Code: "1101", Name: "Cash on Hand", SubCategoryID: ptr(1),
			SubCategoryCode: ptr(1100), CategoryID: 1, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1201", Code: "1201", Name: "Land", SubCategoryID: ptr(2),
			SubCategoryCode: ptr(1200), CategoryID: 1, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: false, IsVisible: true, IsPostable: true},
		{ID: "uuid-4101", Code: "4101", Name: "Product Sales", SubCategoryID: ptr(7),
			SubCategoryCode: ptr(4100), CategoryID: 4, CategoryCode: 4000, BSPNL: "PNL", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
	}
	return cats, subs, accts
}

func TestBuildTreeGroupsBySection(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})

	require.Len(t, got, 2)
	assert.Equal(t, "BS", got[0].BSPNL)
	assert.Equal(t, "PNL", got[1].BSPNL)

	require.Len(t, got[0].Categories, 1)
	assert.Equal(t, 1000, got[0].Categories[0].Code)
	require.Len(t, got[0].Categories[0].SubCategories, 2)
	assert.Equal(t, 1100, got[0].Categories[0].SubCategories[0].Code)
	assert.Equal(t, 1200, got[0].Categories[0].SubCategories[1].Code)
}

func TestBuildTreeOrdersAccountsByCode(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})

	current := got[0].Categories[0].SubCategories[0].Accounts
	require.Len(t, current, 2, "1101 and 1103; 1103.01 nests under 1103")
	assert.Equal(t, "1101", current[0].Code)
	assert.Equal(t, "1103", current[1].Code)
}

func TestBuildTreeNestsChildren(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})

	bank := got[0].Categories[0].SubCategories[0].Accounts[1]
	require.Len(t, bank.Children, 1)
	assert.Equal(t, "1103.01", bank.Children[0].Code)
	assert.Equal(t, "HDFC USA", bank.Children[0].Name)
}

func TestBuildTreeExcludesInactiveByDefault(t *testing.T) {
	cats, subs, accts := treeFixture()
	got := BuildTree(cats, subs, accts, TreeOptions{})

	fixed := got[0].Categories[0].SubCategories[1]
	assert.Equal(t, 1200, fixed.Code)
	assert.Empty(t, fixed.Accounts, "1201 Land is inactive")
}

func TestBuildTreeExcludesHidden(t *testing.T) {
	cats, subs, accts := treeFixture()
	accts[2].IsVisible = false // 1101 Cash on Hand
	accts[2].IsActive = false  // active implies visible, so retire it too

	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})
	codes := []string{}
	for _, a := range got[0].Categories[0].SubCategories[0].Accounts {
		codes = append(codes, a.Code)
	}
	assert.NotContains(t, codes, "1101")

	got = BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true, IncludeHidden: true})
	codes = []string{}
	for _, a := range got[0].Categories[0].SubCategories[0].Accounts {
		codes = append(codes, a.Code)
	}
	assert.Contains(t, codes, "1101")
}

// A child whose parent was filtered out must still appear rather than vanish
// silently -- an account missing from a financial report is worse than one
// shown at the wrong indent.
func TestBuildTreePromotesOrphans(t *testing.T) {
	cats, subs, accts := treeFixture()
	accts[0].IsActive = false // 1103, the parent
	accts[0].IsVisible = false

	got := BuildTree(cats, subs, accts, TreeOptions{})
	codes := []string{}
	for _, a := range got[0].Categories[0].SubCategories[0].Accounts {
		codes = append(codes, a.Code)
	}
	assert.Contains(t, codes, "1103.01", "orphaned child must be promoted, not dropped")
}

func TestBuildTreeEmptyInputs(t *testing.T) {
	got := BuildTree(nil, nil, nil, TreeOptions{})
	assert.Empty(t, got)
}

// AD-2: sub-category 9100 holds BS accounts (9101, 9102) AND PNL accounts
// (9103-9107). It must appear under BOTH sections, each carrying only its own
// accounts. Assigning the whole sub-category one side would file five P&L
// accounts on the balance sheet -- the exact failure bs_pnl-per-account exists
// to prevent.
func TestBuildTreeSplitsMixedSubCategory(t *testing.T) {
	cats := []Category{{ID: 9, Code: 9000, Name: "System & Control Accounts",
		NormalBalance: "debit", BSPNL: MixedSide, SortOrder: 9}}
	subs := []SubCategory{{ID: 17, CategoryID: 9, CategoryCode: 9000, Code: 9100,
		Name: "System & Control Accounts", SortOrder: 1}}
	accts := []*Account{
		{ID: "u-9101", Code: "9101", Name: "Opening Balance Equity", SubCategoryID: ptr(17),
			SubCategoryCode: ptr(9100), CategoryID: 9, CategoryCode: 9000, BSPNL: "BS",
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "u-9102", Code: "9102", Name: "Suspense Account", SubCategoryID: ptr(17),
			SubCategoryCode: ptr(9100), CategoryID: 9, CategoryCode: 9000, BSPNL: "BS",
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "u-9103", Code: "9103", Name: "Rounding Adjustment", SubCategoryID: ptr(17),
			SubCategoryCode: ptr(9100), CategoryID: 9, CategoryCode: 9000, BSPNL: "PNL",
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "u-9104", Code: "9104", Name: "Inventory Adjustment", SubCategoryID: ptr(17),
			SubCategoryCode: ptr(9100), CategoryID: 9, CategoryCode: 9000, BSPNL: "PNL",
			IsActive: true, IsVisible: true, IsPostable: true},
	}

	got := BuildTree(cats, subs, accts, TreeOptions{})
	require.Len(t, got, 2, "9100 must appear under both sections")

	codesIn := func(sec TreeSection) []string {
		var out []string
		for _, c := range sec.Categories {
			for _, s := range c.SubCategories {
				for _, a := range s.Accounts {
					out = append(out, a.Code)
				}
			}
		}
		return out
	}
	assert.Equal(t, "BS", got[0].BSPNL)
	assert.ElementsMatch(t, []string{"9101", "9102"}, codesIn(got[0]))
	assert.Equal(t, "PNL", got[1].BSPNL)
	assert.ElementsMatch(t, []string{"9103", "9104"}, codesIn(got[1]),
		"P&L accounts must not appear on the balance sheet")
}

// Ordering must not depend on Go's randomised map iteration.
func TestBuildTreeIsDeterministic(t *testing.T) {
	cats, subs, accts := treeFixture()
	first := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})
	for i := 0; i < 20; i++ {
		again := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})
		require.Len(t, again, len(first))
		for s := range first {
			assert.Equal(t, first[s].BSPNL, again[s].BSPNL)
			require.Len(t, again[s].Categories, len(first[s].Categories))
			for c := range first[s].Categories {
				assert.Equal(t, first[s].Categories[c].Code, again[s].Categories[c].Code)
				var wantSubs, gotSubs []int
				for _, sc := range first[s].Categories[c].SubCategories {
					wantSubs = append(wantSubs, sc.Code)
				}
				for _, sc := range again[s].Categories[c].SubCategories {
					gotSubs = append(gotSubs, sc.Code)
				}
				assert.Equal(t, wantSubs, gotSubs)
			}
		}
	}
}

func TestBuildTreeKeepsEmptySubCategories(t *testing.T) {
	cats, subs, _ := treeFixture()
	got := BuildTree(cats, subs, nil, TreeOptions{})
	require.Len(t, got, 2)
	assert.Len(t, got[0].Categories[0].SubCategories, 2,
		"structure is shown even when no accounts fall under it")
}

// A MIXED category is the one with no fixed BS/PNL side -- DeriveBSPNL errors
// for it, since 9100 mixes BS accounts (9101/9102) and PNL accounts
// (9103-9107). fixedSide falls back to hardcoded BalanceSheet when there are
// zero accounts to derive a side from. This is a deliberate, documented choice
// (tree.go fixedSide); this test pins it down so an empty 9100 appears exactly
// once, under Balance Sheet, never under P&L.
func TestBuildTreeEmptyMixedCategoryFallsBackToBalanceSheet(t *testing.T) {
	cats := []Category{{ID: 9, Code: 9000, Name: "System & Control Accounts",
		NormalBalance: "debit", BSPNL: MixedSide, SortOrder: 9}}
	subs := []SubCategory{{ID: 17, CategoryID: 9, CategoryCode: 9000, Code: 9100,
		Name: "System & Control Accounts", SortOrder: 1}}

	got := BuildTree(cats, subs, nil, TreeOptions{})
	require.Len(t, got, 1, "9100 with no accounts must appear under exactly one section")
	assert.Equal(t, BalanceSheet, got[0].BSPNL)
	require.Len(t, got[0].Categories, 1)
	require.Len(t, got[0].Categories[0].SubCategories, 1)
	assert.Equal(t, 9100, got[0].Categories[0].SubCategories[0].Code)
	assert.Empty(t, got[0].Categories[0].SubCategories[0].Accounts)
}

// An account placed directly on a category is reported on the category node
// itself, above its sub-categories, rather than being forced through one.
func TestBuildTreeReportsCategoryDirectAccounts(t *testing.T) {
	cats, subs, accts := treeFixture()
	accts = append(accts,
		&Account{ID: "uuid-1001", Code: "1001", Name: "Assets Control", CategoryID: 1,
			CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: false},
		&Account{ID: "uuid-1000", Code: "1000", Name: "Assets Summary", CategoryID: 1,
			CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: false},
	)

	got := BuildTree(cats, subs, accts, TreeOptions{IncludeInactive: true})
	assets := got[0].Categories[0]

	require.Len(t, assets.Accounts, 2)
	assert.Equal(t, "1000", assets.Accounts[0].Code, "direct accounts sort by code")
	assert.Equal(t, "1001", assets.Accounts[1].Code)
	assert.Len(t, assets.SubCategories, 2, "sub-categories are unaffected")
	for _, s := range assets.SubCategories {
		for _, a := range s.Accounts {
			assert.NotEqual(t, "1000", a.Code, "a direct account must not also appear under a sub-category")
			assert.NotEqual(t, "1001", a.Code)
		}
	}
}

// A sub-account of a category-direct account nests under it exactly as one
// under a sub-category-placed account does.
func TestBuildTreeNestsChildrenOfCategoryDirectAccounts(t *testing.T) {
	cats, subs, _ := treeFixture()
	parent := "uuid-1000"
	accts := []*Account{
		{ID: "uuid-1000", Code: "1000", Name: "Assets Summary", CategoryID: 1,
			CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: false},
		{ID: "uuid-1000-01", Code: "1000.01", Name: "Assets Sub", CategoryID: 1,
			CategoryCode: 1000, BSPNL: "BS", Depth: 1, ParentID: &parent,
			IsActive: true, IsVisible: true, IsPostable: true},
	}

	got := BuildTree(cats, subs, accts, TreeOptions{})
	assets := got[0].Categories[0]
	require.Len(t, assets.Accounts, 1)
	require.Len(t, assets.Accounts[0].Children, 1)
	assert.Equal(t, "1000.01", assets.Accounts[0].Children[0].Code)
}

// A category a tenant just created has neither sub-categories nor accounts. It
// must still be reported, or there is nowhere in the UI to add anything to it.
func TestBuildTreeKeepsEmptyCategories(t *testing.T) {
	cats := []Category{
		{ID: 10, Code: 10000, Name: "Statistical", NormalBalance: "debit", BSPNL: BalanceSheet, SortOrder: 10},
		{ID: 11, Code: 11000, Name: "Memo", NormalBalance: "credit", BSPNL: ProfitAndLoss, SortOrder: 11},
	}

	got := BuildTree(cats, nil, nil, TreeOptions{})
	require.Len(t, got, 2)
	assert.Equal(t, BalanceSheet, got[0].BSPNL)
	require.Len(t, got[0].Categories, 1)
	assert.Equal(t, 10000, got[0].Categories[0].Code)
	assert.Empty(t, got[0].Categories[0].SubCategories)
	assert.Empty(t, got[0].Categories[0].Accounts)

	assert.Equal(t, ProfitAndLoss, got[1].BSPNL)
	require.Len(t, got[1].Categories, 1)
	assert.Equal(t, 11000, got[1].Categories[0].Code)
}
