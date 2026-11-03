package saml

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPublicAddr(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"fe80::1", false},
		{"fdaa:0:1::3", false}, // Fly private network (ULA)
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"::ffff:127.0.0.1", false}, // v4-mapped loopback
		{"::ffff:8.8.8.8", true},
		{"64:ff9b::a00:1", false}, // NAT64 of 10.0.0.1
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			assert.Equal(t, tt.want, isPublicAddr(netip.MustParseAddr(tt.ip)))
		})
	}
}

func TestGuardDial(t *testing.T) {
	require.ErrorIs(t, guardDial("tcp", "127.0.0.1:443", nil), ErrMetadataAddressBlocked)
	require.ErrorIs(t, guardDial("tcp6", "[fdaa::3]:443", nil), ErrMetadataAddressBlocked)
	require.NoError(t, guardDial("tcp", "8.8.8.8:443", nil))
	require.Error(t, guardDial("tcp", "not-an-address", nil))
}

// trustServer makes client trust srv's self-signed TLS certificate while
// keeping every other client setting (guard, redirect policy, timeout).
func trustServer(t *testing.T, client *http.Client, srv *httptest.Server) *http.Client {
	t.Helper()
	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	return client
}

func TestFetchIdPMetadata_RefusesLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()

	_, err := fetchIdPMetadata(context.Background(), trustServer(t, newMetadataClient(), srv), srv.URL)
	require.ErrorIs(t, err, ErrMetadataAddressBlocked)
	assert.False(t, hit, "the guard must refuse before any request is sent")
}

func TestFetchIdPMetadata_RefusesRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()

	// The test server lives on loopback, so swap in an unguarded dialer to
	// reach it; the redirect policy is what's under test here.
	client := trustServer(t, newMetadataClient(), srv)
	client.Transport.(*http.Transport).DialContext = nil

	_, err := fetchIdPMetadata(context.Background(), client, srv.URL)
	require.ErrorIs(t, err, ErrMetadataRedirect)
}
