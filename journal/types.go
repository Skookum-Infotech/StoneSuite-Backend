package journal

import "time"

// LineInput is one caller-supplied debit or credit leg of an entry to create.
// Exactly one of Debit/Credit must be positive; the other must be zero.
type LineInput struct {
	AccountID int
	Debit     float64
	Credit    float64
}

// CreateEntryInput is what CreateEntry needs to post a new, balanced entry.
type CreateEntryInput struct {
	EntryDate              time.Time
	Memo                   string
	SourceType             string // e.g. "cash_transfer" — spec AD-2, no FK
	SourceID               string // the source document's UUID
	IsReversal             bool
	ReversesJournalEntryID int // internal journal_entry_id being reversed; 0 when not a reversal
	CreatedBy              int // employee id; 0 = unknown
	Lines                  []LineInput
}

// JournalEntryLine is one persisted leg of a posted entry.
type JournalEntryLine struct {
	LineNumber int
	AccountID  int
	Debit      float64
	Credit     float64
}

// JournalEntry is a persisted, balanced posting. InternalID is the row's
// serial PK — callers (document-module store layers) store this in their own
// header's journal_entry_id FK column; UUID is for display.
type JournalEntry struct {
	InternalID int
	UUID       string
	Number     string
	EntryDate  time.Time
	Memo       string
	SourceType string
	SourceID   string
	IsReversal bool
	Lines      []JournalEntryLine
}
