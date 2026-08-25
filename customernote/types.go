// Package customernote implements customer-submitted notes: a thin,
// append-mostly sub-resource of a CRM customer record (see the `customer`
// table), written by an authenticated external customer (customer_identities)
// and triaged by tenant staff. No lifecycle beyond a plain status field, no
// approval, no money math — structurally the customer-authored counterpart
// to crmactivity.
package customernote

import "time"

// ValidStatuses is the fixed status enum (mirrors the chk_customer_note_status
// CHECK constraint in schema.sql).
var ValidStatuses = map[string]bool{
	"new":      true,
	"read":     true,
	"resolved": true,
}

// CreateNoteInput is the customer-facing create-request payload.
type CreateNoteInput struct {
	Body string `json:"body"`
}

// UpdateStatusInput is the staff-facing status-update payload.
type UpdateStatusInput struct {
	Status string `json:"status"`
}

// SubmitterRef is the light {id, name, email} reference to the customer
// identity that submitted a note.
type SubmitterRef struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// CustomerNote is the full API response for one customer-submitted note.
type CustomerNote struct {
	ID        string       `json:"id"`
	RecordID  string       `json:"recordId"` // parent CRM customer record's external uuid
	Body      string       `json:"body"`
	Status    string       `json:"status"`
	Submitter SubmitterRef `json:"submitter"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}
