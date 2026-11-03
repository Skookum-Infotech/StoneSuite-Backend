package provisioning

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestR2BucketIdempotencyKey(t *testing.T) {
	tests := []struct{ name, tenant, want string }{
		{"uuid", "abc-123", "tenant_r2_bucket:abc-123"},
		{"empty", "", "tenant_r2_bucket:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, R2BucketIdempotencyKey(tt.tenant))
		})
	}
}

func TestR2BucketJobPayloadRoundTrip(t *testing.T) {
	in := R2BucketJob{TenantID: "t1", Slug: "acme"}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var out R2BucketJob
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, in, out)
	assert.JSONEq(t, `{"tenantId":"t1","slug":"acme"}`, string(raw))
}
