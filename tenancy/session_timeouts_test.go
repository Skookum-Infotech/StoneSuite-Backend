package tenancy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMillis(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want *string
	}{
		{name: "zero keeps current", in: 0, want: nil},
		{name: "negative keeps current", in: -time.Second, want: nil},
		{name: "seconds", in: 30 * time.Second, want: strPtr("30000ms")},
		{name: "sub-second", in: 250 * time.Millisecond, want: strPtr("250ms")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, millis(tt.in))
		})
	}
}

func TestSessionTimeouts_IsZero(t *testing.T) {
	tests := []struct {
		name string
		in   SessionTimeouts
		want bool
	}{
		{name: "empty", in: SessionTimeouts{}, want: true},
		{name: "statement only", in: SessionTimeouts{Statement: time.Second}, want: false},
		{name: "lock only", in: SessionTimeouts{Lock: time.Second}, want: false},
		{name: "idle only", in: SessionTimeouts{IdleInTx: time.Second}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.in.IsZero())
		})
	}
}

func strPtr(s string) *string { return &s }
