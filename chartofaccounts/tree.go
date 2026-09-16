package chartofaccounts

import "sort"

// TreeOptions toggles what the report includes. The zero value is the default
// view: active, visible accounts only.
type TreeOptions struct {
	IncludeInactive bool
	IncludeHidden   bool
}

// TreeAccount is one account in the report, with its children nested.
type TreeAccount struct {
	*Account
	Children []*TreeAccount `json:"children"`
}

// TreeSubCategory groups accounts under a fixed sub-category.
type TreeSubCategory struct {
	ID       int            `json:"id"`
	Code     int            `json:"code"`
	Name     string         `json:"name"`
	Accounts []*TreeAccount `json:"accounts"`
}

// TreeCategory groups sub-categories under a category. Accounts holds the
// accounts placed directly on the category, rendered above its sub-categories.
type TreeCategory struct {
	ID            int                `json:"id"`
	Code          int                `json:"code"`
	Name          string             `json:"name"`
	NormalBalance string             `json:"normalBalance"`
	Accounts      []*TreeAccount     `json:"accounts"`
	SubCategories []*TreeSubCategory `json:"subCategories"`
}

// TreeSection is the top split: balance sheet vs profit and loss.
type TreeSection struct {
	BSPNL      string          `json:"bsPnl"`
	Label      string          `json:"label"`
	Categories []*TreeCategory `json:"categories"`
}

// sectionLabels renders the two BS/PNL markers for display.
var sectionLabels = map[string]string{
	BalanceSheet:  "Balance Sheet",
	ProfitAndLoss: "Profit & Loss",
}

// BuildTree assembles flat rows into the report structure:
//
//	BS/PNL -> category -> [direct accounts] -> sub-category -> account -> children
//
// Sub-categories are kept even when empty, so the report shows the tenant's
// full account structure rather than only the parts currently populated, and a
// category with neither sub-categories nor direct accounts still appears -- a
// freshly created one must be visible to be worth adding anything to.
//
// A section appears only when at least one of its categories does. An account
// whose parent was filtered out is promoted to top level rather than dropped:
// silently omitting an account from a financial report is worse than showing
// it at the wrong indent.
func BuildTree(cats []Category, subs []SubCategory, accts []*Account, opts TreeOptions) []TreeSection {
	visible := filterAccounts(accts, opts)

	// Index children by parent uuid, keeping only parents that survived.
	present := make(map[string]bool, len(visible))
	for _, a := range visible {
		present[a.ID] = true
	}
	childrenOf := make(map[string][]*Account)
	var roots []*Account
	for _, a := range visible {
		if a.ParentID != nil && present[*a.ParentID] {
			childrenOf[*a.ParentID] = append(childrenOf[*a.ParentID], a)
			continue
		}
		roots = append(roots, a) // top-level, or an orphan we promote
	}

	// Split roots by placement: an account carries a sub-category id or a
	// category id, never both (chk_coa_placement).
	rootsBySub := make(map[int][]*Account, len(subs))
	rootsByCategory := make(map[int][]*Account, len(cats))
	for _, a := range roots {
		if a.SubCategoryID != nil {
			rootsBySub[*a.SubCategoryID] = append(rootsBySub[*a.SubCategoryID], a)
			continue
		}
		rootsByCategory[a.CategoryID] = append(rootsByCategory[a.CategoryID], a)
	}

	subsByCategory := make(map[int][]SubCategory, len(cats))
	for _, s := range subs {
		subsByCategory[s.CategoryID] = append(subsByCategory[s.CategoryID], s)
	}

	byBSPNL := map[string][]*TreeCategory{}
	for _, c := range cats {
		grouped := map[string]*TreeCategory{}
		// One category can appear on both sides (see the AD-2 note below), so
		// every append goes through here rather than creating the node inline.
		nodeFor := func(side string) *TreeCategory {
			tc, ok := grouped[side]
			if !ok {
				// Accounts/SubCategories start as non-nil empty slices, not the
				// zero-value nil -- encoding/json marshals a nil slice as
				// "null", and a category with nothing placed directly on it
				// (true of every category today; direct placement is brand
				// new) would otherwise send "accounts":null. The frontend
				// renders cat.accounts unconditionally, so that null crashes
				// the whole tree on page load. wrap() below exists for the
				// identical reason on TreeAccount.Children.
				tc = &TreeCategory{
					ID: c.ID, Code: c.Code, Name: c.Name, NormalBalance: c.NormalBalance,
					Accounts:      make([]*TreeAccount, 0),
					SubCategories: make([]*TreeSubCategory, 0),
				}
				grouped[side] = tc
			}
			return tc
		}

		// Accounts placed directly on the category, partitioned the same way
		// its sub-categories' accounts are.
		for _, side := range []string{BalanceSheet, ProfitAndLoss} {
			for _, a := range sortByCode(rootsByCategory[c.ID]) {
				if a.BSPNL != side {
					continue
				}
				tc := nodeFor(side)
				tc.Accounts = append(tc.Accounts, &TreeAccount{
					Account:  a,
					Children: wrap(sortByCode(childrenOf[a.ID])),
				})
			}
		}

		for _, s := range subsByCategory[c.ID] {
			// Partition the sub-category's accounts by EACH ACCOUNT's own side,
			// not by one side for the whole sub-category. Sub-category 9100
			// holds both (9101/9102 are BS, 9103-9107 are PNL), and assigning
			// it a single side would file five P&L accounts on the balance
			// sheet -- the exact failure bs_pnl lives on the account to prevent
			// (AD-2). Every other sub-category yields exactly one partition.
			bySide := map[string][]*Account{}
			for _, a := range sortByCode(rootsBySub[s.ID]) {
				bySide[a.BSPNL] = append(bySide[a.BSPNL], a)
			}
			if len(bySide) == 0 {
				// No accounts, but the structure is still shown, on the side
				// the sub-category inherits from its category.
				bySide[fixedSide(c.BSPNL)] = nil
			}

			// Iterate the sides in fixed order; ranging a map would make the
			// output order non-deterministic between runs.
			for _, side := range []string{BalanceSheet, ProfitAndLoss} {
				accts, present := bySide[side]
				if !present {
					continue
				}
				// Same non-nil-empty-slice reasoning as TreeCategory.Accounts
				// above: a genuinely empty sub-category (kept visible by
				// design -- see the doc comment on BuildTree) must still send
				// "accounts":[], not "accounts":null.
				ts := &TreeSubCategory{ID: s.ID, Code: s.Code, Name: s.Name, Accounts: make([]*TreeAccount, 0)}
				for _, a := range accts {
					ts.Accounts = append(ts.Accounts, &TreeAccount{
						Account:  a,
						Children: wrap(sortByCode(childrenOf[a.ID])),
					})
				}
				nodeFor(side).SubCategories = append(nodeFor(side).SubCategories, ts)
			}
		}

		// An empty category still shows, on its own side.
		if len(grouped) == 0 {
			nodeFor(fixedSide(c.BSPNL))
		}

		for side, tc := range grouped {
			sort.SliceStable(tc.SubCategories, func(i, j int) bool {
				return tc.SubCategories[i].Code < tc.SubCategories[j].Code
			})
			byBSPNL[side] = append(byBSPNL[side], tc)
		}
	}

	var out []TreeSection
	for _, side := range []string{BalanceSheet, ProfitAndLoss} {
		tcs := byBSPNL[side]
		if len(tcs) == 0 {
			continue
		}
		sort.SliceStable(tcs, func(i, j int) bool { return tcs[i].Code < tcs[j].Code })
		out = append(out, TreeSection{BSPNL: side, Label: sectionLabels[side], Categories: tcs})
	}
	return out
}

// filterAccounts applies the visibility toggles.
func filterAccounts(accts []*Account, opts TreeOptions) []*Account {
	out := make([]*Account, 0, len(accts))
	for _, a := range accts {
		if !a.IsVisible && !opts.IncludeHidden {
			continue
		}
		if !a.IsActive && !opts.IncludeInactive {
			continue
		}
		out = append(out, a)
	}
	return out
}

// sortByCode orders accounts by code. Codes are zero-padded ("1103.09" before
// "1103.10"), so lexical order matches numeric order.
func sortByCode(in []*Account) []*Account {
	out := append([]*Account(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// wrap lifts plain accounts into leaf tree nodes. The tree is capped at two
// levels (AD-4), so a child never has children of its own.
func wrap(in []*Account) []*TreeAccount {
	out := make([]*TreeAccount, 0, len(in))
	for _, a := range in {
		out = append(out, &TreeAccount{Account: a, Children: nil})
	}
	return out
}

// fixedSide reports the side an EMPTY sub-category or category is displayed
// under. It is only consulted when there are no accounts to partition by; when
// there are, each account's own BSPNL decides. A MIXED category (9000 among the
// seeded rows) has no fixed side, so an empty one shows under the balance sheet.
func fixedSide(categorySide string) string {
	if side, err := DeriveBSPNL(categorySide, ""); err == nil {
		return side
	}
	return BalanceSheet
}
