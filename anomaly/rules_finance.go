package anomaly

// Rule keys for the ledger and finance rules, stored in anomaly_rule_key.
const (
	RulePostedInClosedPeriod = "posted_in_closed_period"
	RuleUnusualCashTransfer  = "unusual_cash_transfer"
	RuleChartOfAccountsEdit  = "chart_of_accounts_changed"
)

func financeRules() []Rule {
	return []Rule{
		// A journal entry dated inside a period that was already closed when it was posted.
		sqlRule{
			key: RulePostedInClosedPeriod, severity: SeverityCritical, title: "%s was posted into a closed period",
			audience: audience{superAdmins: true},
			args:     []any{lookbackDays},
			query: `
				SELECT je.journal_entry_uuid::text, je.journal_entry_uuid::text, 'chart_of_account',
				       'Journal entry ' || COALESCE(je.journal_entry_number, ''),
				       'Dated ' || to_char(je.entry_date, 'YYYY-MM-DD') || ' in ' || ap.accounting_period_name
				         || ', which was closed on ' || to_char(ap.accounting_period_closed_at, 'YYYY-MM-DD') || ' before this was posted.',
				       ` + involvedNone + `, ua.identity_id::text, je.journal_entry_created_at
				FROM journal_entry je
				JOIN accounting_period ap ON je.entry_date BETWEEN ap.period_start AND ap.period_end
				     AND ap.accounting_period_status = 'closed'
				     AND je.journal_entry_created_at > ap.accounting_period_closed_at
				LEFT JOIN employee ea ON ea.employee_id = je.journal_entry_created_by
				LEFT JOIN users ua ON ua.id = ea.employee_user_id
				WHERE je.journal_entry_created_at >= $3::date - $4::int`,
		},

		// A cash transfer far larger than this workspace's usual ones.
		sqlRule{
			key: RuleUnusualCashTransfer, severity: SeverityWarning, title: "%s is much larger than usual",
			audience: audience{superAdmins: true, owner: true},
			args:     []any{cashTransferHistory, cashTransferMultiple, cashTransferFloor, lookbackDays},
			query: `
				SELECT ct.cash_transfer_uuid::text, ct.cash_transfer_uuid::text, 'cash_transfer',
				       'Cash transfer ' || COALESCE(ct.cash_transfer_number, ''),
				       'Transfer of ' || ct.cash_transfer_amount::text || ' against a usual ' || round(h.med::numeric, 2)::text
				         || ' (median of ' || h.n::text || ' earlier transfers).',
				       ` + ownerIdentity + `, ` + involvedNone + `, ct.cash_transfer_created_at
				FROM cash_transfer ct
				JOIN lkp_record_status st ON st.record_status_id = ct.cash_transfer_status AND st.record_status_code IN ('APPR', 'POST')
				JOIN LATERAL (
				    SELECT COUNT(*) AS n, percentile_cont(0.5) WITHIN GROUP (ORDER BY o.cash_transfer_amount) AS med
				    FROM cash_transfer o
				    WHERE o.cash_transfer_id < ct.cash_transfer_id AND o.cash_transfer_deleted_at IS NULL) h
				     ON h.n >= $4::int AND h.med > 0
				` + ownerJoin("ct", "cash_transfer_owner_id") + `
				WHERE ct.cash_transfer_deleted_at IS NULL AND ct.cash_transfer_created_at >= $3::date - $7::int
				  AND ct.cash_transfer_amount >= $6::numeric AND ct.cash_transfer_amount > $5::numeric * h.med`,
		},

		// The chart of accounts was edited. Information only: it appears on the page, not in the bell.
		sqlRule{
			key: RuleChartOfAccountsEdit, severity: SeverityInfo, title: "%s was changed",
			audience: audience{superAdmins: true},
			args:     []any{coaChangeDays},
			query: `
				SELECT 'coa:' || h.coa_account_history_id::text, a.coa_account_uuid::text, 'chart_of_account',
				       'Account ' || COALESCE(a.coa_account_code || ' ' || a.coa_account_name, 'in the chart of accounts'),
				       'Action: ' || h.history_action || COALESCE(' on ' || h.history_field, '') || ' on ' || to_char(h.history_at, 'YYYY-MM-DD') || '.',
				       ` + involvedNone + `, uh.identity_id::text, h.history_at
				FROM coa_account_history h
				LEFT JOIN coa_account a ON a.coa_account_id = h.coa_account_id
				LEFT JOIN employee eh ON eh.employee_id = h.history_by
				LEFT JOIN users uh ON uh.id = eh.employee_user_id
				WHERE h.history_at >= $3::date - $4::int`,
		},
	}
}
