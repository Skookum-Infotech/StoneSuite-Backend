package models

// APIResponse is the standard wrapper for simple backend JSON responses.
// Richer payloads (tokens, users, lists) are returned as purpose-built structs
// or maps by the individual handlers.
type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}
