package journal

import (
	"context"
	"fmt"
	"time"
)

// ReverseEntry loads originalJournalEntryID's lines and posts a new entry
// with every line's debit and credit swapped, crediting back exactly what the
// original debited and vice versa, so the balance impact of the pair nets to
// zero. The new entry carries the same source_type/source_id as the
// original, plus is_reversal=true and reverses_journal_entry_id pointing back
// at it.
func ReverseEntry(ctx context.Context, q querier, originalJournalEntryID int, reversalDate time.Time, memo string, createdBy int) (*JournalEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT jel.coa_account_id, jel.debit, jel.credit
		FROM journal_entry_line jel
		WHERE jel.journal_entry_id = $1
		ORDER BY jel.line_number`, originalJournalEntryID)
	if err != nil {
		return nil, fmt.Errorf("load original journal entry lines: %w", err)
	}
	var swapped []LineInput
	for rows.Next() {
		var accountID int
		var debit, credit float64
		if err := rows.Scan(&accountID, &debit, &credit); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan original journal entry line: %w", err)
		}
		swapped = append(swapped, LineInput{AccountID: accountID, Debit: credit, Credit: debit})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate original journal entry lines: %w", err)
	}
	if len(swapped) == 0 {
		return nil, ErrInvalidLine
	}

	var sourceType, sourceID string
	if err := q.QueryRow(ctx,
		`SELECT source_type, source_id FROM journal_entry WHERE journal_entry_id = $1`,
		originalJournalEntryID,
	).Scan(&sourceType, &sourceID); err != nil {
		return nil, fmt.Errorf("load original journal entry source: %w", err)
	}

	return CreateEntry(ctx, q, CreateEntryInput{
		EntryDate:              reversalDate,
		Memo:                   memo,
		SourceType:             sourceType,
		SourceID:               sourceID,
		IsReversal:             true,
		ReversesJournalEntryID: originalJournalEntryID,
		CreatedBy:              createdBy,
		Lines:                  swapped,
	})
}
