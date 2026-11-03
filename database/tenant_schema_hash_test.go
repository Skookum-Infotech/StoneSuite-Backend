package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTenantSchemaHash(t *testing.T) {
	h := TenantSchemaHash()
	assert.Len(t, h, 64)
	assert.Regexp(t, "^[0-9a-f]{64}$", h)
	assert.Equal(t, h, TenantSchemaHash())
}
