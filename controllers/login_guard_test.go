package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/bcrypt"
)

func TestRetryAfterSeconds(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want int
	}{
		{"already expired", -time.Second, 1},
		{"zero", 0, 1},
		{"sub-second rounds up", 200 * time.Millisecond, 1},
		{"fraction rounds up", 59*time.Second + time.Millisecond, 60},
		{"whole minutes", 2 * time.Minute, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, retryAfterSeconds(tt.in))
		})
	}
}

// The dummy hash must be a valid bcrypt hash at the real cost, or the
// comparison returns instantly and the timing equalisation is lost.
func TestDummyPasswordHashCost(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyPasswordHash))
	assert.NoError(t, err)
	assert.Equal(t, bcrypt.DefaultCost, cost)
}
