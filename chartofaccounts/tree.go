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

// TreeCategory groups sub-categories under a fixed category.
type TreeCategory struct {
	ID            int                `json:"id"`
	Code          int                `json:"code"`
	Name          string             `json:"name"`
	NormalBalance string             `json:"normalBalance"`
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
//	BS/PNL -> category -> sub-category -> account -> children
//
// Sub-categories are kept even when empty, so the report shows the tenant's
// full account structure rather than only the parts currently populated.
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

	rootsBySub := make(map[int][]*Account, len(subs))
	for _, a := range roots {
		rootsBySub[a.SubCategoryID] = append(rootsBySub[a.SubCategoryID], a)
	}

	subsByCategory := make(map[int][]SubCategory, len(cats))
	for _, s := range subs {
		subsByCategory[s.CategoryID] = append(subsByCategory[s.CategoryID], s)
	}

	byBSPNL := map[string][]*TreeCategory{}
	for _, c := range cats {
		grouped := map[string]*TreeCategory{}
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
				// the sub-category is fixed to.
				bySide[fixedSide(s.Code)] = nil
			}

			// Iterate the sides in fixed order; ranging a map would make the
			// output order non-deterministic between runs.
			for _, side := range []string{BalanceSheet, ProfitAndLoss} {
				accts, present := bySide[side]
				if !present {
					continue
				}
				ts := &TreeSubCategory{ID: s.ID, Code: s.Code, Name: s.Name}
				for _, a := range accts {
					ts.Accounts = append(ts.Accounts, &TreeAccount{
						Account:  a,
						Children: wrap(sortByCode(childrenOf[a.ID])),
					})
				}
				tc, ok := grouped[side]
				if !ok {
					tc = &TreeCategory{ID: c.ID, Code: c.Code, Name: c.Name, NormalBalance: c.NormalBalance}
					grouped[side] = tc
				}
				tc.SubCategories = append(tc.SubCategories, ts)
			}
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

// fixedSide reports the side an EMPTY sub-category is displayed under. It is
// only consulted when a sub-category has no accounts to partition by; when it
// has accounts, each account's own BSPNL decides. Sub-category 9100 has no
// fixed side (DeriveBSPNL errors for it), so an empty 9100 shows under the
// balance sheet.
func fixedSide(subCategoryCode int) string {
	if side, err := DeriveBSPNL(subCategoryCode, ""); err == nil {
		return side
	}
	return BalanceSheet
}
