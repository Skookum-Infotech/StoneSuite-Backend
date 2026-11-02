package middleware

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUserRateLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewUserRateLimiter(0.1, 3)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		assert.True(t, l.Allow("a"), "burst %d", i)
	}
	assert.False(t, l.Allow("a"), "burst exhausted")
	assert.True(t, l.Allow("b"), "other key independent")

	now = now.Add(10 * time.Second)
	assert.True(t, l.Allow("a"), "one token refilled")
	assert.False(t, l.Allow("a"))
	assert.Equal(t, 10, l.RetryAfterSeconds())

	now = now.Add(time.Hour)
	assert.True(t, l.Allow("c"))
	assert.NotContains(t, l.buckets, "a", "idle bucket evicted lazily")
}
