package models

// APIResponse is the standard wrapper for simple backend JSON responses.
// Richer payloads (tokens, users, lists) are returned as purpose-built structs
// or maps by the individual handlers.
type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	// Code is a stable machine-readable reason for an error a client handles
	// differently from others with the same status (e.g. two kinds of 429).
	// Omitted when a status code alone says enough.
	Code string `json:"code,omitempty"`
}

// CodeRateLimited marks a 429 from a request-rate limiter: the caller should
// slow down, not merely retry.
const CodeRateLimited = "rate_limited"
