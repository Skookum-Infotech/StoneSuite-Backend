package proposedaction

import (
	"errors"

	"stonesuite-backend/authz"
)

// Sentinel errors the HTTP controller maps to status codes.
var (
	// ErrNotFound: no such proposal for this caller (404).
	ErrNotFound = errors.New("proposed action not found")
	// ErrExpired: the proposal passed its expiry (410/409).
	ErrExpired = errors.New("proposed action expired")
	// ErrConflict: the proposal is not in a state that allows the operation (409).
	ErrConflict = errors.New("proposed action is not pending")
	// ErrForbidden: the caller may not confirm this proposal (403).
	ErrForbidden = errors.New("not permitted to confirm proposed action")
	// ErrInvalidPayload: the payload failed executor validation (400/422).
	ErrInvalidPayload = errors.New("invalid proposed action payload")
	// ErrExecutionFailed: executing the confirmed action failed (500).
	ErrExecutionFailed = errors.New("proposed action execution failed")
	// ErrInvalidCursor: the list cursor is malformed (400).
	ErrInvalidCursor = errors.New("invalid cursor")
)

// NotFoundError is a not-found that records whether the row exists but the
// caller may not reach it (another identity's proposal, or a target record
// outside the caller's scope), so the handler can log proposed_action_idor while still answering 404.
type NotFoundError struct{ Mismatch bool }

// Error implements error.
func (e *NotFoundError) Error() string { return ErrNotFound.Error() }

// Is matches ErrNotFound.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ForbiddenError carries the permission that was missing or out of scope.
type ForbiddenError struct {
	Resource authz.Resource
	Action   authz.Action
}

// Error implements error.
func (e *ForbiddenError) Error() string { return ErrForbidden.Error() }

// Is matches ErrForbidden.
func (e *ForbiddenError) Is(target error) bool { return target == ErrForbidden }

// ValidationError wraps an executor's validation message.
type ValidationError struct{ Message string }

// Error implements error.
func (e *ValidationError) Error() string { return ErrInvalidPayload.Error() + ": " + e.Message }

// Is matches ErrInvalidPayload.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidPayload }
