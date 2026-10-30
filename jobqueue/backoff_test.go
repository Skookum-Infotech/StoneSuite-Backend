package jobqueue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{"negative", -3, 5 * time.Second},
		{"zero", 0, 5 * time.Second},
		{"one", 1, 10 * time.Second},
		{"two", 2, 20 * time.Second},
		{"five", 5, 160 * time.Second},
		{"nine", 9, 2560 * time.Second},
		{"ten hits cap", 10, time.Hour},
		{"just over shift bound", 13, time.Hour},
		{"overflow guard", 1000, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BackoffDelay(tt.attempts))
		})
	}
}
