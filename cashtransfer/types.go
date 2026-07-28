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
}

// Page is one keyset-paginated slice of cash transfers.
type Page struct {
	Records    []CashTransfer `json:"records"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}
