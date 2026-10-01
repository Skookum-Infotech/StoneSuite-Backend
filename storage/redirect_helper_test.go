package storage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirectTransport sends every request to srv regardless of the URL it was
// built for, so code that hardcodes https://{host}/... can be exercised against
// an httptest server without a test-only seam in production code.
func redirectTransport(t *testing.T, srv *httptest.Server) http.RoundTripper {
	t.Helper()
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
}

// redirectDefaultClient points http.DefaultClient (which Client uses) at srv
// for the duration of the test.
func redirectDefaultClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := http.DefaultClient.Transport
	http.DefaultClient.Transport = redirectTransport(t, srv)
	t.Cleanup(func() { http.DefaultClient.Transport = prev })
}
