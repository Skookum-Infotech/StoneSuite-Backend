package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeRoute(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"static path unchanged", "/api/tenant/workflows", "/api/tenant/workflows"},
		{"uuid collapsed", "/api/tenant/records/9f3c1b2a-1111-2222-3333-444455556666", "/api/tenant/records/{id}"},
		{"numeric id collapsed", "/api/tenant/config/approvers/42", "/api/tenant/config/approvers/{id}"},
		{"trailing subresource preserved", "/api/tenant/records/9f3c1b2a-1111-2222-3333-444455556666/transition", "/api/tenant/records/{id}/transition"},
		{"long opaque token collapsed", "/api/onboarding/apply/abcdef0123456789abcdef0123456789", "/api/onboarding/apply/{id}"},
		{"short slug preserved", "/api/tenant/crm/lead/records", "/api/tenant/crm/lead/records"},
		{"root", "/", "/"},
		{"empty", "", "/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeRoute(tc.in))
		})
	}
}

func TestIsIDSegment(t *testing.T) {
	assert.True(t, isIDSegment("123"))
	assert.True(t, isIDSegment("9f3c1b2a-1111-2222-3333-444455556666"))
	assert.True(t, isIDSegment("abcdef0123456789abcdef0123456789"))
	assert.False(t, isIDSegment("records"))
	assert.False(t, isIDSegment("lead"))
	assert.False(t, isIDSegment(""))
}
