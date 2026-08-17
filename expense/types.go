// Package expense: relational store for the Expense module -- an employee's
// self-service reimbursement claim (header + dated/categorized line items),
// gated through a configuration-driven approval before Finance marks it
// reimbursed. A sibling of requisition/purchaseorder, not the generic v1
// JSONB workflow engine.
// Spec: docs/superpowers/specs/2026-08-17-expense-module-design.md
package expense

import "time"

// LineInput is one expense entry on create/update -- a single dated,
// categorized, described amount (typically backed by one receipt, attached
// separately via the generic /api/tenant/records/{uuid}/attachments/* API).
type LineInput struct {
	LineNumber   int     `json:"lineNumber"`
	CategoryCode string  `json:"categoryCode"`
	ExpenseDate  string  `json:"expenseDate"` // "yyyy-mm-dd"
	Amount       float64 `json:"amount"`
	Description  string  `json:"description"`
}

// expenseFields is the header payload shared by create and update.
type expenseFields struct {
	Department   string         `json:"department"`
	Memo         string         `json:"memo"`
	CustomFields map[string]any `json:"customFields"`
	Items        []LineInput    `json:"items"`
}

// CreateExpenseInput is the create-request payload. The claimant is never
// taken as input -- it is always the acting caller's own resolved employee
// (AD-2: self-service, mirrors requisition's owner-is-requester pattern).
type CreateExpenseInput struct {
	expenseFields
}

// UpdateExpenseInput is the update-request payload (DRFT only).
type UpdateExpenseInput struct {
	expenseFields
}

// Line is one expense entry in the API response.
type Line struct {
	ID           string  `json:"id"`
	LineNumber   int     `json:"lineNumber"`
	CategoryID   int     `json:"categoryId"`
	CategoryCode string  `json:"categoryCode"`
	CategoryName string  `json:"categoryName"`
	ExpenseDate  string  `json:"expenseDate"`
	Amount       float64 `json:"amount"`
	Description  string  `json:"description"`
}

// Expense is the full API response for an expense claim header (+ lines,
// when loaded by Get). OwnerUserID backs the controller's IDOR scope check
// (the claimant is the scope owner) and is never serialized.
type Expense struct {
	ID             string `json:"id"`
	Number         string `json:"expenseNumber"`
	Status         string `json:"status"`         // human label, e.g. "Draft"
	StatusCode     string `json:"statusCode"`     // lkp_record_status code, e.g. "DRFT"
	ApprovalStatus string `json:"approvalStatus"` // none | pending | approved
	OwnerUserID    string `json:"-"`

	ClaimantEmployeeID int    `json:"claimantEmployeeId"`
	Department         string `json:"department"`
	Memo               string `json:"memo,omitempty"`

	ApprovedByEmployeeID *int   `json:"approvedByEmployeeId,omitempty"`
	RejectedByEmployeeID *int   `json:"rejectedByEmployeeId,omitempty"`
	RejectionReason      string `json:"rejectionReason,omitempty"`

	CustomFields map[string]any `json:"customFields,omitempty"`

	Total float64 `json:"total"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Items     []Line    `json:"items,omitempty"`
}

// Page is one page of a keyset-paginated expense search. List rows omit
// Items (search selects header columns only, to avoid an N+1 line-item
// join) -- only Get loads the full claim with lines.
type Page struct {
	Records    []Expense
	NextCursor string
	HasMore    bool
}
