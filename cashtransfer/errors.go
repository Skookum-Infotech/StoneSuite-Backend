package cashtransfer

import "errors"

// ErrNotFound is returned when a cash transfer id matches no live row.
var ErrNotFound = errors.New("cash transfer not found")

// ErrAlreadyPosted is returned when Post is called on a transfer that has
// already moved through POST or RVSD (spec: prevent duplicate posting).
var ErrAlreadyPosted = errors.New("cash transfer is already posted")

// ErrNotPosted is returned when Reverse is called on a transfer that was
// never posted (spec: reverse only valid for posted transfers).
var ErrNotPosted = errors.New("cash transfer has not been posted")

// ErrPeriodClosed is returned when Post or Reverse's effective date falls
// within the closed accounting period (spec AD-4).
var ErrPeriodClosed = errors.New("accounting period is closed for this date")

// ErrVersionMismatch is returned when UpdateInput.RecordVersion is nonzero
// and does not match the row's current cash_transfer_record_version (spec §6:
// "recordVersion mismatch" -> 409), mirroring chartofaccounts.ConflictError.
var ErrVersionMismatch = errors.New("cash transfer was changed by someone else")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }
