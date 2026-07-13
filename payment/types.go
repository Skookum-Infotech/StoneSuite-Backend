package payment

import "time"

// CustomerRef is the flattened {id, name} for "who paid" navigation.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Application is one live payment_application row, joined with its invoice's
// display fields.
type Application struct {
	ID            string    `json:"id"`
	InvoiceID     string    `json:"invoiceId"`
	InvoiceNumber string    `json:"invoiceNumber"`
	Amount        float64   `json:"amount"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Payment is the payment header + its live applications.
type Payment struct {
	ID     string `json:"id"`
	Number string `json:"paymentNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Customer CustomerRef `json:"customer"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	MethodID   int    `json:"methodId"`
	MethodName string `json:"method"`

	ReferenceNumber string    `json:"referenceNumber"`
	PaymentDate     time.Time `json:"paymentDate"`
	CurrencyID      *int      `json:"currencyId,omitempty"`
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

// ApplicationInput is one entry of a create/apply request.
type ApplicationInput struct {
	InvoiceUUID string  `json:"invoiceUuid"`
	Amount      float64 `json:"amount"`
}

// CreatePaymentInput is the request payload for POST /api/tenant/payments.
type CreatePaymentInput struct {
	CustomerUUID    string             `json:"customerUuid"`
	MethodID        int                `json:"methodId"`
	ReferenceNumber string             `json:"referenceNumber"`
	PaymentDate     *time.Time         `json:"paymentDate,omitempty"`
	CurrencyID      *int               `json:"currencyId,omitempty"`
	OwnerEmployeeID *int               `json:"ownerEmployeeId,omitempty"`
	Amount          float64            `json:"amount"`
	Memo            string             `json:"memo"`
	InternalNotes   string             `json:"internalNotes"`
	CustomFields    map[string]any     `json:"customFields"`
	Applications    []ApplicationInput `json:"applications"`
}

// UpdatePaymentInput is the request payload for PATCH /api/tenant/payments/{uuid}.
// Notice it has no Amount field (spec AD-10: amount is immutable post-creation).
type UpdatePaymentInput struct {
	MethodID        int            `json:"methodId"`
	ReferenceNumber string         `json:"referenceNumber"`
	PaymentDate     *time.Time     `json:"paymentDate,omitempty"`
	CurrencyID      *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one page of payments plus keyset-pagination state.
type Page struct {
	Records    []Payment `json:"records"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}
