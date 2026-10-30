package saml

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// The metadata URL is typed by a tenant admin, so fetching it is a request
// the server makes on someone else's behalf. Without a guard it reaches
// anything the machine can: Fly's private network (Ollama, other apps), the
// metadata endpoint at 169.254.169.254, localhost. The guard runs at dial
// time, after DNS resolution, so a public hostname that resolves (or
// re-resolves) to an internal address is refused too, and redirects are
// refused outright so an allowed host can't bounce the request inward.

// metadataDialTimeout bounds TCP connect inside the overall fetch timeout.
const metadataDialTimeout = 5 * time.Second

// ErrMetadataAddressBlocked is returned when the metadata host resolves to a
// loopback, private, link-local or otherwise non-public address.
var ErrMetadataAddressBlocked = errors.New("saml: idp metadata host is not a public address")

// ErrMetadataRedirect is returned when the metadata URL answers with a redirect.
var ErrMetadataRedirect = errors.New("saml: idp metadata url redirected; use the final url")

// nonPublicPrefixes are ranges netip's own predicates don't cover.
func nonPublicPrefixes() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
		netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
		netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
		netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
		netip.MustParsePrefix("240.0.0.0/4"),   // reserved, incl. broadcast
		netip.MustParsePrefix("64:ff9b::/96"),  // NAT64 (can embed an internal v4)
	}
}

// isPublicAddr reports whether ip is a globally routable unicast address.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublicPrefixes() {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// guardDial is a net.Dialer Control hook: it sees the resolved ip:port of
// every connection attempt and refuses non-public ones before connecting.
func guardDial(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("saml: parsing dial address: %w", err)
	}
	if !isPublicAddr(ap.Addr()) {
		return ErrMetadataAddressBlocked
	}
	return nil
}

// newMetadataClient returns the HTTP client FetchIdPMetadata uses: overall
// timeout, no redirects, no proxy (a proxy would dial on our behalf and
// bypass the guard), and only public destination addresses.
func newMetadataClient() *http.Client {
	dialer := &net.Dialer{Timeout: metadataDialTimeout, Control: guardDial}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:   metadataFetchTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrMetadataRedirect
		},
	}
}
