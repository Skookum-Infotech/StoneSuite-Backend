package anomaly

import (
	"context"
	"fmt"
	"time"

	"stonesuite-backend/authz"
)

// UnbalancedJournal flags a journal entry whose debits do not equal its
// credits. Double-entry bookkeeping makes that impossible by construction, so
// one existing means a posting bug or a manual database edit: critical.
type UnbalancedJournal struct{}

// Key implements Rule.
func (UnbalancedJournal) Key() string { return RuleUnbalancedJournal }

// Detect implements Rule.
func (UnbalancedJournal) Detect(ctx context.Context, q Querier, _ time.Time) ([]Finding, error) {
	rows, err := q.Query(ctx, `
		SELECT je.journal_entry_uuid::text, COALESCE(je.journal_entry_number, ''),
		       SUM(l.debit)::text, SUM(l.credit)::text
		FROM journal_entry je
		JOIN journal_entry_line l ON l.journal_entry_id = je.journal_entry_id
		WHERE NOT EXISTS (
		    SELECT 1 FROM anomaly a
		    WHERE a.anomaly_rule_key = $1 AND a.anomaly_subject_key = je.journal_entry_uuid::text)
		GROUP BY je.journal_entry_id, je.journal_entry_uuid, je.journal_entry_number
		HAVING SUM(l.debit) <> SUM(l.credit)
		ORDER BY je.journal_entry_id DESC
		LIMIT $2`, RuleUnbalancedJournal, MaxFindingsPerRule)
	if err != nil {
		return nil, fmt.Errorf("unbalanced journal scan: %w", err)
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		var entryUUID, number, debits, credits string
		if err := rows.Scan(&entryUUID, &number, &debits, &credits); err != nil {
			return nil, fmt.Errorf("unbalanced journal scan: scan: %w", err)
		}
		out = append(out, Finding{
			RuleKey:    RuleUnbalancedJournal,
			SubjectKey: entryUUID,
			Severity:   SeverityCritical,
			Resource:   string(authz.ResourceChartOfAccount),
			RecordUUID: entryUUID,
			Title:      unbalancedJournalTitle(number),
			Detail:     fmt.Sprintf("Debits total %s but credits total %s. The ledger no longer balances until this entry is corrected.", debits, credits),
			Audience:   Audience{SuperAdmins: true},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unbalanced journal scan: iterate: %w", err)
	}
	return out, nil
}

// unbalancedJournalTitle names the entry, falling back to a generic label when
// it has no number.
func unbalancedJournalTitle(number string) string {
	if number == "" {
		return "A journal entry does not balance"
	}
	return fmt.Sprintf("Journal entry %s does not balance", number)
}
