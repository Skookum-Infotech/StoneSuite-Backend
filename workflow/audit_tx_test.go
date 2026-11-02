package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditIP(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ipv4", "203.0.113.7", "203.0.113.7"},
		{"ipv6", "2001:db8::1", "2001:db8::1"},
		{"garbage", "not-an-ip", ""},
		{"with port", "203.0.113.7:443", ""},
		{"list header", "203.0.113.7, 10.0.0.1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, auditIP(tt.in))
		})
	}
}

func TestLogAuditTx_NoRequestIsNoOp(t *testing.T) {
	// A nil tx is safe: the function returns before touching it.
	err := LogAuditTx(context.Background(), nil, "create", "invoice", "1", "invoice", nil, nil, nil)
	require.NoError(t, err)
}

func TestAuditRequestRoundTrip(t *testing.T) {
	_, ok := AuditRequestFrom(context.Background())
	assert.False(t, ok)
	want := AuditRequest{ActorUserID: "u", IPAddress: "203.0.113.7", SessionID: "s", AppVersion: "v"}
	got, ok := AuditRequestFrom(WithAuditRequest(context.Background(), want))
	require.True(t, ok)
	assert.Equal(t, want, got)
}
