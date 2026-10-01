package mytransactions

import "stonesuite-backend/authz"

// financeSources lists the finance modules. The frontend surfaces cash_transfer
// as "Journal Entries" (finance/journal-entries); journal/ itself is the GL
// posting engine, not a browsable record.
func financeSources() []Source {
	// Chart of accounts is gated by RBAC resource only (no owner column, no
	// status): every account on a read grant is visible.
	account := Source{
		Key: "chart_of_account", Label: "Account", Resource: authz.ResourceChartOfAccount,
		Domain: "finance", Module: "chart-of-accounts",
		Table:     "coa_account",
		ID:        "t.coa_account_uuid",
		Number:    "t.coa_account_code",
		Name:      "t.coa_account_name",
		CreatedBy: "coa_account_created_by", UpdatedBy: "coa_account_updated_by",
		CreatedAt: "coa_account_created_at", UpdatedAt: "coa_account_updated_at",
		DeletedAt: "coa_account_deleted_at",
	}

	transfer := doc("cash_transfer", "Journal Entry", authz.ResourceCashTransfer,
		"finance", "journal-entries", "cash_transfer", "cash_transfer")
	transfer.Amount = "t.cash_transfer_amount"

	return []Source{account, transfer}
}
