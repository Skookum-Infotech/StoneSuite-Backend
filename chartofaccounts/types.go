// Package chartofaccounts implements the Chart of Accounts master-data module:
// a fixed category/sub-category reference tree, 127 seeded accounts, a
// two-level user-extensible account tree, and named default-account mapping
// slots. It holds no balances -- the general ledger is a separate module.
//
// Spec: docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md
package chartofaccounts

import "time"

// Account is one chart-of-accounts entry.
type Account struct {
	ID              string         `json:"id"` // uuid
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	SubCategoryID   int            `json:"subCategoryId"`
	SubCategoryCode int            `json:"subCategoryCode"`
	SubCategoryName string         `json:"subCategoryName"`
	CategoryCode    int            `json:"categoryCode"`
	CategoryName    string         `json:"categoryName"`
	ParentID        *string        `json:"parentId,omitempty"` // uuid
	Depth           int            `json:"depth"`
	BSPNL           string         `json:"bsPnl"` // "BS" | "PNL"
	Type            string         `json:"type"`
	Attributes      map[string]any `json:"attributes"`
	IsPostable      bool           `json:"isPostable"`
	IsActive        bool           `json:"isActive"`
	IsVisible       bool           `json:"isVisible"`
	IsSystem        bool           `json:"isSystem"`
	RecordVersion   int            `json:"recordVersion"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// Category is a fixed top-level classification (1000 Assets ... 9000 System).
type Category struct {
	ID            int    `json:"id"`
	Code          int    `json:"code"`
	Name          string `json:"name"`
	RangeLow      int    `json:"rangeLow"`
	RangeHigh     int    `json:"rangeHigh"`
	NormalBalance string `json:"normalBalance"` // "debit" | "credit"
	SortOrder     int    `json:"sortOrder"`
}

// SubCategory is a fixed second-level classification (1100 Current Assets ...).
type SubCategory struct {
	ID           int    `json:"id"`
	CategoryID   int    `json:"categoryId"`
	CategoryCode int    `json:"categoryCode"`
	Code         int    `json:"code"`
	Name         string `json:"name"`
	RangeLow     int    `json:"rangeLow"`
	RangeHigh    int    `json:"rangeHigh"`
	SortOrder    int    `json:"sortOrder"`
}

// DefaultSlot is a named mapping from a posting purpose to one account.
type DefaultSlot struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Description string    `json:"description"`
	AccountID   *string   `json:"accountId,omitempty"` // uuid
	AccountCode string    `json:"accountCode,omitempty"`
	AccountName string    `json:"accountName,omitempty"`
	IsSystem    bool      `json:"isSystem"`
	SortOrder   int       `json:"sortOrder"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateInput is the payload for creating an account. Code, depth and
// bs_pnl are server-assigned; bs_pnl is accepted ONLY under sub-category
// 9100, the one sub-category that mixes BS and PNL (AD-2).
type CreateInput struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	SubCategoryID int            `json:"subCategoryId"` // required when ParentID is empty
	ParentID      string         `json:"parentId"`      // uuid; empty means top-level
	BSPNL         string         `json:"bsPnl"`         // required only under 9100
	Type          string         `json:"type"`
	Attributes    map[string]any `json:"attributes"`
	IsPostable    *bool          `json:"isPostable"`
}

// UpdateInput is the payload for PATCHing an account. Nil fields are left
// unchanged. Code, sub-category and parent are immutable after create.
type UpdateInput struct {
	Name        *string        `json:"name"`
	Description *string        `json:"description"`
	Type        *string        `json:"type"`
	Attributes  map[string]any `json:"attributes"`
	IsPostable  *bool          `json:"isPostable"`
	IsActive    *bool          `json:"isActive"`
	IsVisible   *bool          `json:"isVisible"`
	// RecordVersion is the optimistic-concurrency token: send back the version
	// you last read and Update rejects a stale write with 409. It is a plain
	// int, so an omitted field and an explicit 0 are indistinguishable, and
	// coa_account_record_version starts at 1 -- so 0 deliberately means "no
	// version check requested," not "expect version 0." A caller that omits it
	// gets no concurrency protection: two concurrent callers who both omit it
	// and change the same field will last-write-wins silently.
	RecordVersion int `json:"recordVersion"`
}

// BulkInput toggles visibility flags across many accounts in one transaction.
type BulkInput struct {
	UUIDs     []string `json:"uuids"`
	IsActive  *bool    `json:"isActive"`
	IsVisible *bool    `json:"isVisible"`
}

// BulkResult is the per-account outcome of a bulk update.
type BulkResult struct {
	UUID    string `json:"uuid"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// HistoryEntry is one audited change to an account or a default slot.
type HistoryEntry struct {
	ID        int       `json:"id"`
	AccountID *string   `json:"accountId,omitempty"`
	SlotKey   string    `json:"slotKey,omitempty"`
	Action    string    `json:"action"`
	Field     string    `json:"field"`
	OldValue  string    `json:"oldValue"`
	NewValue  string    `json:"newValue"`
	At        time.Time `json:"at"`
	By        *int      `json:"by,omitempty"`
}
