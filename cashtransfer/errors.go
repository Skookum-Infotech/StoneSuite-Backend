package cashtransfer

import (
	"errors"

	"stonesuite-backend/journal"
)

// ErrNotFound is returned when a cash transfer id matches no live row.
var ErrNotFound = errors.New("cash transfer not found")

// ErrAlreadyPosted is returned when Post is called on a transfer that has
// already moved through POST or RVSD (spec: prevent duplicate posting).
var ErrAlreadyPosted = errors.New("cash transfer is already posted")

// ErrNotPosted is returned when Reverse is called on a transfer that was
// never posted (spec: reverse only valid for posted transfers).
var ErrNotPosted = errors.New("cash transfer has not been posted")

// ErrPeriodClosed is returned when Post or Reverse's effective date falls
// within a closed accounting period (spec AD-4).
var ErrPeriodClosed = errors.New("accounting period is closed for this date")

// ErrNoAccountingPeriod is returned when the tenant has a fiscal calendar but
// no period covers the effective date — typically posting past the last
// generated fiscal year. Distinct from ErrPeriodClosed because the remedy is
// to generate the next fiscal year, not to reopen a period.
var ErrNoAccountingPeriod = errors.New("no accounting period exists for this date")

// translatePeriodError maps journal's period sentinels onto this package's
// own, so controllers keep matching on cashtransfer errors only and never have
// to import journal.
func translatePeriodError(err error) error {
	switch {
	case errors.Is(err, journal.ErrPeriodClosed):
		return ErrPeriodClosed
	case errors.Is(err, journal.ErrNoAccountingPeriod):
		return ErrNoAccountingPeriod
	default:
		return err
	}
}

// ErrVersionMismatch is returned when UpdateInput.RecordVersion is nonzero
// and does not match the row's current cash_transfer_record_version (spec §6:
// "recordVersion mismatch" -> 409), mirroring chartofaccounts.ConflictError.
var ErrVersionMismatch = errors.New("cash transfer was changed by someone else")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }
