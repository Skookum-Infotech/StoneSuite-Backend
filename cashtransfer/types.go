package cashtransfer

import "time"

// AccountRef is the flattened {id, code, name} for an account reference.
type AccountRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// CashTransfer is the full record returned by Get/Search.
type CashTransfer struct {
	ID     string `json:"id"`
	Number string `json:"transferNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	TransferDate time.Time `json:"transferDate"`

	FromAccount AccountRef `json:"fromAccount"`
	ToAccount   AccountRef `json:"toAccount"`

	Amount    float64 `json:"amount"`
	Reference string  `json:"reference"`

	Notes         string `json:"notes"`
	InternalNotes string `json:"internalNotes"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	CustomFields map[string]any `json:"customFields"`

	JournalEntryID         *string    `json:"journalEntryId,omitempty"`
	ReversalJournalEntryID *string    `json:"reversalJournalEntryId,omitempty"`
	PostedAt               *time.Time `json:"postedAt,omitempty"`
	ReversedAt             *time.Time `json:"reversedAt,omitempty"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// CreateInput is the request payload for POST /api/tenant/finance/cash-transfers.
type CreateInput struct {
	FromAccountUUID string         `json:"fromAccountUuid"`
	ToAccountUUID   string         `json:"toAccountUuid"`
	Amount          float64        `json:"amount"`
	TransferDate    *time.Time     `json:"transferDate,omitempty"`
	Reference       string         `json:"reference"`
	Notes           string         `json:"notes"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	CustomFields    map[string]any `json:"customFields"`
}

// UpdateInput is the request payload for PATCH .../{uuid} (Draft only —
// unlike payment, amount IS editable here because nothing has posted yet).
type UpdateInput struct {
	FromAccountUUID string         `json:"fromAccountUuid"`
	ToAccountUUID   string         `json:"toAccountUuid"`
	Amount          float64        `json:"amount"`
	TransferDate    *time.Time     `json:"transferDate,omitempty"`
	Reference       string         `json:"reference"`
	Notes           string         `json:"notes"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	CustomFields    map[string]any `json:"customFields"`
	// RecordVersion is the optimistic-concurrency token: send back the version
	// you last read and Update rejects a stale write with ErrVersionMismatch
	// (409). It is a plain int, so an omitted field and an explicit 0 are
	// indistinguishable, and cash_transfer_record_version starts at 1 -- so 0
	// deliberately means "no version check requested," not "expect version 0."
	// Mirrors chartofaccounts.UpdateInput.RecordVersion.
	RecordVersion int `json:"recordVersion"`
}

// Page is one keyset-paginated slice of cash transfers.
type Page struct {
	Records    []CashTransfer `json:"records"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}
