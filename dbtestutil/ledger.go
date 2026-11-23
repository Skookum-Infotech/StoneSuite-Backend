package dbtestutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LedgerEntries counts a document's general-ledger entries by source_type and
// source uuid: live (non-reversal) entries and reversals.
func LedgerEntries(t testing.TB, pool *pgxpool.Pool, sourceType, sourceUUID string) (live, reversals int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FILTER (WHERE NOT is_reversal), COUNT(*) FILTER (WHERE is_reversal)
		FROM journal_entry WHERE source_type = $1 AND source_id = $2::uuid`,
		sourceType, sourceUUID).Scan(&live, &reversals); err != nil {
		t.Fatalf("count ledger entries for %s %s: %v", sourceType, sourceUUID, err)
	}
	return live, reversals
}

// SlotBalance is the running balance (debits positive, credits negative) of the
// account a default-account slot (coa_default_mapping.slot_key) points at. Tests
// compare it before and after, since other tests' postings share the database.
func SlotBalance(t testing.TB, pool *pgxpool.Pool, slotKey string) float64 {
	t.Helper()
	var b float64
	if err := pool.QueryRow(context.Background(), `
		SELECT a.coa_account_balance FROM coa_default_mapping m
		JOIN coa_account a ON a.coa_account_id = m.coa_account_id
		WHERE m.slot_key = $1`, slotKey).Scan(&b); err != nil {
		t.Fatalf("balance of default account %s: %v", slotKey, err)
	}
	return b
}

// EntryLines returns the account-slot totals of a document's live entry as
// debit-minus-credit per slot key, for asserting the exact shape of a posting.
func EntryLines(t testing.TB, pool *pgxpool.Pool, sourceType, sourceUUID string) map[string]float64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT m.slot_key, SUM(l.debit - l.credit)
		FROM journal_entry je
		JOIN journal_entry_line l ON l.journal_entry_id = je.journal_entry_id
		JOIN coa_default_mapping m ON m.coa_account_id = l.coa_account_id
		WHERE je.source_type = $1 AND je.source_id = $2::uuid AND NOT je.is_reversal
		GROUP BY m.slot_key`, sourceType, sourceUUID)
	if err != nil {
		t.Fatalf("read entry lines for %s %s: %v", sourceType, sourceUUID, err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var slot string
		var net float64
		if err := rows.Scan(&slot, &net); err != nil {
			t.Fatalf("scan entry line: %v", err)
		}
		out[slot] = net
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read entry lines: %v", err)
	}
	return out
}
