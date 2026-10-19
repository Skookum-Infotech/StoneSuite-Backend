package recordemail

import "time"

// Direction says which way a message travelled.
type Direction string

const (
	// DirectionOut is mail StoneSuite sent (the Send-to-customer email, or a
	// reply typed into the Communication tab).
	DirectionOut Direction = "out"
	// DirectionIn is a customer's reply that arrived on the record's address.
	DirectionIn Direction = "in"
)

// Message is one email in a record's conversation. Bodies are plain text only.
type Message struct {
	ID              string    `json:"id"`
	RecordID        string    `json:"recordId"`
	WorkflowKey     string    `json:"workflowKey"`
	Direction       Direction `json:"direction"`
	FromName        string    `json:"fromName"`
	FromEmail       string    `json:"fromEmail"`
	To              string    `json:"to"`
	CC              string    `json:"cc"`
	Subject         string    `json:"subject"`
	Body            string    `json:"body"`
	AttachmentCount int       `json:"attachmentCount"`
	CreatedAt       time.Time `json:"createdAt"`

	// Threading and bookkeeping fields: stored, never sent to the browser.
	MessageID       string `json:"-"`
	InReplyTo       string `json:"-"`
	References      string `json:"-"`
	ProviderEmailID string `json:"-"`
	SentByUserID    string `json:"-"`
	DocumentSendID  string `json:"-"`
}
