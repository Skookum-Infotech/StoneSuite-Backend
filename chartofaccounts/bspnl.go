package chartofaccounts

import "fmt"

// Balance-sheet vs profit-and-loss markers, matching chk_coa_bs_pnl.
const (
	BalanceSheet  = "BS"
	ProfitAndLoss = "PNL"
)

// MixedSide marks a category whose accounts do not all share one side, so each
// account carries its own (AD-2). 9000 System & Control is the only seeded one:
// 9101 Opening Balance Equity and 9102 Suspense are balance-sheet accounts,
// 9103-9107 are P&L. Stored in lkp_coa_category.category_bs_pnl.
const MixedSide = "MIXED"

// MixedSubCategoryCode is sub-category 9100, the seeded sub-category under the
// one MIXED category. Kept as a named constant because the seed data and the
// dbtest fixtures still refer to it by code; derivation itself no longer keys
// off it.
const MixedSubCategoryCode = 9100

// DeriveBSPNL returns the BS/PNL side for an account placed under a category
// whose own side is categorySide.
//
// The side used to come from a package-level map keyed by the 17 seeded
// sub-category codes. That map could not answer for a sub-category a tenant
// created, so the side now lives on the category row and every sub-category
// inherits its parent category's -- which is what the map encoded anyway
// (1100/1200/1300 all BS under 1000 Assets, 6100-6500 all PNL under 6000
// Operating Expenses, and so on).
//
// For a BS or PNL category the side is derived and any supplied value is
// ignored: a user must not be able to file a revenue account on the balance
// sheet. Under a MIXED category the side is genuinely ambiguous, so supplied is
// required and must be exactly "BS" or "PNL".
func DeriveBSPNL(categorySide, supplied string) (string, error) {
	switch categorySide {
	case BalanceSheet, ProfitAndLoss:
		return categorySide, nil
	case MixedSide:
		switch supplied {
		case BalanceSheet, ProfitAndLoss:
			return supplied, nil
		case "":
			return "", ClientError{Msg: "bsPnl is required for this category, " +
				"which contains both balance-sheet and P&L accounts."}
		default:
			return "", ClientError{Msg: fmt.Sprintf(
				"bsPnl must be %q or %q, got %q.", BalanceSheet, ProfitAndLoss, supplied)}
		}
	default:
		return "", ClientError{Msg: fmt.Sprintf(
			"Unknown category side %q.", categorySide)}
	}
}

// ValidCategorySide reports whether s is one of the three sides a category row
// may declare. Used to validate the side supplied when creating a category.
func ValidCategorySide(s string) bool {
	return s == BalanceSheet || s == ProfitAndLoss || s == MixedSide
}
