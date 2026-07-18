package refund

import "time"

// CustomerRef is the flattened {id, name} for "who is being refunded" navigation.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Application is one live refund_application row. Exactly one of PaymentID or
// CreditMemoID is set (mirrors the chk_refund_app_xor_source DB constraint).
type Application struct {
	ID               string    `json:"id"`
	PaymentID        string    `json:"paymentId,omitempty"`
	PaymentNumber    string    `json:"paymentNumber,omitempty"`
	CreditMemoID     string    `json:"creditMemoId,omitempty"`
	CreditMemoNumber string    `json:"creditMemoNumber,omitempty"`
	Amount           float64   `json:"amount"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Refund is the refund header + its live applications.
type Refund struct {
	ID     string `json:"id"`
	Number string `json:"refundNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Customer CustomerRef `json:"customer"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	// Lineage only (AD-12) — no money semantics. Populated when the refund
	// was created against a specific payment/credit-memo/invoice.
	PaymentID    string `json:"paymentId,omitempty"`
	CreditMemoID string `json:"creditMemoId,omitempty"`
	InvoiceID    string `json:"invoiceId,omitempty"`

	MethodID   int    `json:"methodId"`
	MethodName string `json:"method"`

	ReferenceNumber string    `json:"referenceNumber"`
	RefundDate      time.Time `json:"refundDate"`
	CurrencyID      *int      `json:"currencyId,omitempty"`
	Reason          string    `json:"reason"`
	Memo            string    `json:"memo"`
	InternalNotes   string    `json:"internalNotes"`

	Amount          float64 `json:"amount"`
	AppliedTotal    float64 `json:"appliedTotal"`
	UnappliedAmount float64 `json:"unappliedAmount"`

	CustomFields map[string]any `json:"customFields"`
	Applications []Application  `json:"applications"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// CreateRefundInput is the request payload for POST /api/tenant/refunds. At
// most one of PaymentUUID/CreditMemoUUID may carry an initial application
// (spec §11) — a refund composing both sources is built via two POST
// .../apply calls after create, not in one shot.
type CreateRefundInput struct {
	CustomerUUID    string         `json:"customerUuid"`
	MethodID        int            `json:"methodId"`
	ReferenceNumber string         `json:"referenceNumber"`
	RefundDate      *time.Time     `json:"refundDate,omitempty"`
	CurrencyID      *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Amount          float64        `json:"amount"`
	Reason          string         `json:"reason"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`

	// Lineage (AD-12) — all optional, no money semantics.
	PaymentUUID    string `json:"paymentUuid,omitempty"`
	CreditMemoUUID string `json:"creditMemoUuid,omitempty"`
	InvoiceUUID    string `json:"invoiceUuid,omitempty"`
}

// UpdateRefundInput is the request payload for PATCH /api/tenant/refunds/{uuid}.
// Notice it has no Amount, PaymentUUID, or CreditMemoUUID field (spec AD-8:
// money and source identity are immutable post-creation).
type UpdateRefundInput struct {
	MethodID        int            `json:"methodId"`
	ReferenceNumber string         `json:"referenceNumber"`
	RefundDate      *time.Time     `json:"refundDate,omitempty"`
	CurrencyID      *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Reason          string         `json:"reason"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one page of refunds plus keyset-pagination state.
type Page struct {
	Records    []Refund `json:"records"`
	NextCursor string   `json:"nextCursor"`
	HasMore    bool     `json:"hasMore"`
}
