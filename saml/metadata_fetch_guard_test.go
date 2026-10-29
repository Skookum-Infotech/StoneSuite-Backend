package saml

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.5", false},
		{"172.16.3.4", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"fdaa:0:1::3", false},
		{"fe80::1", false},
	}
	for _, c := range cases {
		t.Run(c.ip, func(t *testing.T) {
			assert.Equal(t, c.want, isPublicIP(net.ParseIP(c.ip)))
		})
	}
}

func TestGuardedDialControl_BlocksLoopback(t *testing.T) {
	assert.ErrorIs(t, guardedDialControl("tcp", "127.0.0.1:443", nil), errBlockedAddress)
	assert.NoError(t, guardedDialControl("tcp", "8.8.8.8:443", nil))
}
