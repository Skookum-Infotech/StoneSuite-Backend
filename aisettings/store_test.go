package aisettings

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlatformEnabled_NilPoolIsEnabled proves a nil control-plane pool (a
// handler built without one, e.g. count-direct dispatch tests) is treated as
// enabled with no query attempted, matching the missing-row default.
func TestPlatformEnabled_NilPoolIsEnabled(t *testing.T) {
	enabled, err := PlatformEnabled(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, enabled)
}

// TestTenantEnabled_NilPoolIsEnabled proves a nil tenant pool is treated as
// enabled with no query attempted, same rationale as the platform switch.
func TestTenantEnabled_NilPoolIsEnabled(t *testing.T) {
	enabled, err := TenantEnabled(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, enabled)
}
