package models

import "time"

// Customer represents a customer account managed by StoneSuite operations.
type Customer struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Industry  string            `json:"industry,omitempty"`
	Website   string            `json:"website,omitempty"`
	Status    string            `json:"status"`
	Contacts  []CustomerContact `json:"contacts,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// CustomerContact captures a named customer contact such as a super admin.
type CustomerContact struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customerId"`
	FullName   string    `json:"fullName"`
	Email      string    `json:"email"`
	Phone      string    `json:"phone,omitempty"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// OnboardingInvite stores invite tokens and lifecycle values.
type OnboardingInvite struct {
	ID           string    `json:"id"`
	CustomerID   string    `json:"customerId"`
	ContactID    string    `json:"contactId,omitempty"`
	ContactEmail string    `json:"contactEmail"`
	Token        string    `json:"token,omitempty"`
	Status       string    `json:"status"`
	ExpiresAt    time.Time `json:"expiresAt"`
	SentAt       time.Time `json:"sentAt,omitempty"`
	AcceptedAt   time.Time `json:"acceptedAt,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// OnboardingAuditLog tracks actions taken during onboarding management.
type OnboardingAuditLog struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customerId"`
	InviteID   string    `json:"inviteId,omitempty"`
	ActorID    string    `json:"actorId,omitempty"`
	ActorEmail string    `json:"actorEmail,omitempty"`
	Action     string    `json:"action"`
	Details    string    `json:"details,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// CreateCustomerRequest defines the payload for creating a new customer record.
type CreateCustomerRequest struct {
	Name            string `json:"name"`
	Industry        string `json:"industry,omitempty"`
	Website         string `json:"website,omitempty"`
	SuperAdminName  string `json:"superAdminName"`
	SuperAdminEmail string `json:"superAdminEmail"`
	SuperAdminPhone string `json:"superAdminPhone,omitempty"`
}

// UpdateCustomerRequest defines the allowed update payload for a customer.
type UpdateCustomerRequest struct {
	Name     string `json:"name,omitempty"`
	Industry string `json:"industry,omitempty"`
	Website  string `json:"website,omitempty"`
	Status   string `json:"status,omitempty"`
}

// CreateContactRequest defines the request body to add a customer contact.
type CreateContactRequest struct {
	FullName string `json:"fullName"`
	Email    string `json:"email"`
	Phone    string `json:"phone,omitempty"`
	Role     string `json:"role,omitempty"`
}

// UpdateContactRequest defines the request body to update a customer contact.
type UpdateContactRequest struct {
	FullName string `json:"fullName,omitempty"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Role     string `json:"role,omitempty"`
}

// SendInviteRequest defines the request payload for sending an onboarding invitation.
type SendInviteRequest struct {
	ContactID      string `json:"contactId,omitempty"`
	ContactEmail   string `json:"contactEmail,omitempty"`
	ExpiresInHours int    `json:"expiresInHours,omitempty"`
}

// CompleteOnboardingRequest defines the request payload for customer self-service onboarding.
type CompleteOnboardingRequest struct {
	Token    string `json:"token"`
	FullName string `json:"fullName"`
	Password string `json:"password"`
}

// CustomerListResponse standardizes paged customer list replies.
type CustomerListResponse struct {
	Success   bool       `json:"success"`
	Customers []Customer `json:"customers"`
}

// InviteListResponse returns invite records.
type InviteListResponse struct {
	Success bool               `json:"success"`
	Invites []OnboardingInvite `json:"invites"`
}

// AuditListResponse returns audit history.
type AuditListResponse struct {
	Success   bool                 `json:"success"`
	AuditLogs []OnboardingAuditLog `json:"auditLogs"`
}
