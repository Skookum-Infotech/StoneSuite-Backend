package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCORSAllowedHeaders(t *testing.T) {
	allowed := strings.Split(corsAllowedHeaders, ", ")
	for _, h := range []string{"Content-Type", "Authorization", "X-CSRF-Token", "Idempotency-Key"} {
		assert.Contains(t, allowed, h)
	}
}
