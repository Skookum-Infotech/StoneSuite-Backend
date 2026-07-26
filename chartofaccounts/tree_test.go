package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func treeFixture() ([]Category, []SubCategory, []*Account) {
	cats := []Category{
		{ID: 1, Code: 1000, Name: "Assets", NormalBalance: "debit", SortOrder: 1},
		{ID: 4, Code: 4000, Name: "Revenue", NormalBalance: "credit", SortOrder: 4},
	}
	subs := []SubCategory{
		{ID: 1, CategoryID: 1, CategoryCode: 1000, Code: 1100, Name: "Current Assets", SortOrder: 1},
		{ID: 2, CategoryID: 1, CategoryCode: 1000, Code: 1200, Name: "Fixed Assets", SortOrder: 2},
		{ID: 7, CategoryID: 4, CategoryCode: 4000, Code: 4100, Name: "Sales", SortOrder: 1},
	}
	parent := "uuid-1103"
	accts := []*Account{
		{ID: "uuid-1103", Code: "1103", Name: "Bank Account - Operating", SubCategoryID: 1,
			SubCategoryCode: 1100, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1103-01", Code: "1103.01", Name: "HDFC USA", SubCategoryID: 1,
			SubCategoryCode: 1100, CategoryCode: 1000, BSPNL: "BS", Depth: 1,
			ParentID: &parent, IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1101", Code: "1101", Name: "Cash on Hand", SubCategoryID: 1,
			SubCategoryCode: 1100, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "uuid-1201", Code: "1201", Name: "Land", SubCategoryID: 2,
			SubCategoryCode: 1200, CategoryCode: 1000, BSPNL: "BS", Depth: 0,
			IsActive: false, IsVisible: true, IsPostable: true},
		{ID: "uuid-4101", Code: "4101", Name: "Product Sales", SubCategoryID: 7,
			SubCategoryCode: 4100, CategoryCode: 4000, BSPNL: "PNL", Depth: 0,
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
		NormalBalance: "debit", SortOrder: 9}}
	subs := []SubCategory{{ID: 17, CategoryID: 9, CategoryCode: 9000, Code: 9100,
		Name: "System & Control Accounts", SortOrder: 1}}
	accts := []*Account{
		{ID: "u-9101", Code: "9101", Name: "Opening Balance Equity", SubCategoryID: 17,
			SubCategoryCode: 9100, CategoryCode: 9000, BSPNL: "BS",
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "u-9102", Code: "9102", Name: "Suspense Account", SubCategoryID: 17,
			SubCategoryCode: 9100, CategoryCode: 9000, BSPNL: "BS",
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "u-9103", Code: "9103", Name: "Rounding Adjustment", SubCategoryID: 17,
			SubCategoryCode: 9100, CategoryCode: 9000, BSPNL: "PNL",
			IsActive: true, IsVisible: true, IsPostable: true},
		{ID: "u-9104", Code: "9104", Name: "Inventory Adjustment", SubCategoryID: 17,
			SubCategoryCode: 9100, CategoryCode: 9000, BSPNL: "PNL",
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
