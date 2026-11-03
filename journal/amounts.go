package journal

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// centsPerUnit converts a money amount to integer cents.
	centsPerUnit = 100
	// moneyDecimals is the number of decimal places a line amount may carry.
	moneyDecimals = 2
	// maxLineAmount is the largest magnitude a DECIMAL(15,2) money column holds.
	maxLineAmount = 1e13 - 0.01
	// maxEntryLines caps lines per entry so the int64 cent sums cannot overflow
	// (1000 lines of the maximum amount is ~1e18 cents, below 9.2e18).
	maxEntryLines = 1000
)

// ClientError marks a caller-fault error (callers map it to HTTP 400/409).
type ClientError struct{ Msg string }

// Error returns the caller-facing message.
func (e ClientError) Error() string { return e.Msg }

// toCents converts a money amount to integer cents. It reports false for NaN,
// Inf, negative, over-bound values, and values with more than two decimal
// places (judged on the shortest exact decimal representation, so there is no
// float tolerance: sub-cent input is refused, never silently rounded).
func toCents(x float64) (int64, bool) {
	if !(x >= 0 && x <= maxLineAmount) { // negated form also rejects NaN
		return 0, false
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i-1 > moneyDecimals {
		return 0, false
	}
	return int64(math.Round(x * centsPerUnit)), true
}

// centSums converts every line to cents and returns the debit and credit
// totals. Each line is rounded to cents before summing.
func centSums(lines []LineInput) (debit, credit int64, err error) {
	if len(lines) > maxEntryLines {
		return 0, 0, ClientError{Msg: fmt.Sprintf("A journal entry can have at most %d lines.", maxEntryLines)}
	}
	for _, l := range lines {
		d, okD := toCents(l.Debit)
		c, okC := toCents(l.Credit)
		if !okD || !okC {
			return 0, 0, ErrInvalidLine
		}
		debit += d
		credit += c
	}
	return debit, credit, nil
}
