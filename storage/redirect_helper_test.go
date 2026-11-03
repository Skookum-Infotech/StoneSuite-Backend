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

// redirectClient returns an HTTP client (with the production timeout shape)
// that sends every request to srv, for injecting into a test Client.
func redirectClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	hc := newHTTPClient(0)
	hc.Transport = redirectTransport(t, srv)
	return hc
}
