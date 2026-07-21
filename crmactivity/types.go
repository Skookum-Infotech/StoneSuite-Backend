// Package crmactivity implements the CRM activity log: a thin, append-mostly
// sub-resource of a CRM record (lead/prospect/customer, all backed by the
// v2 relational `customer` table) — calls, emails, meetings, notes, and
// tasks logged against it. No lifecycle, no approval, no money math: unlike
// the document modules (quote/estimate/salesorder/...), an activity is a
// single flat record, not a header + lines.
package crmactivity

import "time"

// ValidTypes is the fixed activity-type enum (mirrors the
// chk_crm_activity_type CHECK constraint in schema.sql).
var ValidTypes = map[string]bool{
	"call":    true,
	"email":   true,
	"meeting": true,
	"note":    true,
	"task":    true,
}

// activityFields is the payload shared by create and update.
type activityFields struct {
	ActivityType string `json:"activityType"`
	OccurredAt   string `json:"occurredAt,omitempty"` // RFC3339; blank ⇒ now
	Subject      string `json:"subject"`
	Body         string `json:"body"`
}

// CreateActivityInput is the create-request payload.
type CreateActivityInput struct {
	activityFields
}

// UpdateActivityInput is the update-request payload.
type UpdateActivityInput struct {
	activityFields
}

// AuthorRef is the light {id, name} author reference on an Activity response.
type AuthorRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Activity is the full API response for one activity log entry.
type Activity struct {
	ID           string    `json:"id"`
	RecordID     string    `json:"recordId"` // parent CRM record's external uuid
	ActivityType string    `json:"activityType"`
	OccurredAt   time.Time `json:"occurredAt"`
	Author       AuthorRef `json:"author"`
	Subject      string    `json:"subject,omitempty"`
	Body         string    `json:"body,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
