package chartofaccounts

import "fmt"

// MixedSubCategoryCode is sub-category 9100 (System & Control Accounts) -- the
// only sub-category holding both balance-sheet accounts (9101 Opening Balance
// Equity, 9102 Suspense) and P&L accounts (9103-9107). Everywhere else BS/PNL
// follows the category, which is why the flag lives on the account (AD-2).
const MixedSubCategoryCode = 9100

// Balance-sheet vs profit-and-loss markers, matching chk_coa_bs_pnl.
const (
	BalanceSheet  = "BS"
	ProfitAndLoss = "PNL"
)

// bsPnlBySubCategory maps every non-mixed sub-category to its fixed side.
var bsPnlBySubCategory = map[int]string{
	1100: BalanceSheet, 1200: BalanceSheet, 1300: BalanceSheet,
	2100: BalanceSheet, 2200: BalanceSheet,
	3100: BalanceSheet,
	4100: ProfitAndLoss, 4200: ProfitAndLoss,
	5100: ProfitAndLoss,
	6100: ProfitAndLoss, 6200: ProfitAndLoss, 6300: ProfitAndLoss,
	6400: ProfitAndLoss, 6500: ProfitAndLoss,
	7100: ProfitAndLoss,
	8100: ProfitAndLoss,
}

// DeriveBSPNL returns the BS/PNL side for an account in subCategoryCode.
//
// For every sub-category except 9100 the side is derived and any supplied
// value is ignored -- a user must not be able to file a revenue account on the
// balance sheet. Under 9100 the side is genuinely ambiguous, so supplied is
// required and must be exactly "BS" or "PNL".
func DeriveBSPNL(subCategoryCode int, supplied string) (string, error) {
	if side, ok := bsPnlBySubCategory[subCategoryCode]; ok {
		return side, nil
	}
	if subCategoryCode != MixedSubCategoryCode {
		return "", ClientError{Msg: fmt.Sprintf(
			"Unknown sub-category code %d.", subCategoryCode)}
	}
	switch supplied {
	case BalanceSheet, ProfitAndLoss:
		return supplied, nil
	case "":
		return "", ClientError{Msg: fmt.Sprintf(
			"bsPnl is required for sub-category %d (System & Control Accounts), "+
				"which contains both balance-sheet and P&L accounts.", MixedSubCategoryCode)}
	default:
		return "", ClientError{Msg: fmt.Sprintf(
			"bsPnl must be %q or %q, got %q.", BalanceSheet, ProfitAndLoss, supplied)}
	}
}
