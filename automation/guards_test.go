package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type timeoutNetErr struct{ timeout bool }

func (e timeoutNetErr) Error() string   { return "net err" }
func (e timeoutNetErr) Timeout() bool   { return e.timeout }
func (e timeoutNetErr) Temporary() bool { return false }

func TestGuardBackoffFor(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 5 * time.Minute},
		{2, 30 * time.Minute},
		{3, 2 * time.Hour},
		{4, 2 * time.Hour},
		{5, 2 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.attempts), func(t *testing.T) {
			assert.Equal(t, tc.want, backoffFor(tc.attempts))
		})
	}
}

func TestGuardIsTimeoutErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"deadline", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("send: %w", context.DeadlineExceeded), true},
		{"net timeout", timeoutNetErr{timeout: true}, true},
		{"wrapped net timeout", fmt.Errorf("x: %w", timeoutNetErr{timeout: true}), true},
		{"net non-timeout", timeoutNetErr{}, false},
		{"canceled", context.Canceled, false},
		{"plain", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTimeoutErr(tc.err))
		})
	}
}

func TestGuardTruncateError(t *testing.T) {
	long := strings.Repeat("a", 600)
	assert.Len(t, truncateError(long), maxLastError)
	assert.Equal(t, "short", truncateError("short"))
	multi := strings.Repeat("é", 400) // 800 bytes
	got := truncateError(multi)
	assert.LessOrEqual(t, len(got), maxLastError)
	assert.True(t, strings.HasPrefix(multi, got))
}

func TestGuardCapReached(t *testing.T) {
	tests := []struct {
		count, limit int
		want         bool
	}{{0, 3, false}, {2, 3, false}, {3, 3, true}, {4, 3, true}, {0, 0, true}}
	for _, tc := range tests {
		assert.Equal(t, tc.want, capReached(tc.count, tc.limit), "%d/%d", tc.count, tc.limit)
	}
}

func TestGuardLocalDayStart(t *testing.T) {
	chi, err := time.LoadLocation("America/Chicago")
	assert.NoError(t, err)
	today := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC), localDayStart(today, chi).UTC())
	assert.Equal(t, today, localDayStart(today, time.UTC).UTC())
}
