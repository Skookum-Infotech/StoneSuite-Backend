package saml

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

const (
	// metadataMaxRedirects bounds redirect chains on an IdP metadata fetch.
	metadataMaxRedirects = 3
	// metadataDialTimeout bounds TCP connect to the metadata host.
	metadataDialTimeout = 5 * time.Second
)

// errBlockedAddress is returned when a metadata URL resolves to a non-public
// address (loopback, private, link-local, Fly 6PN, metadata service...).
var errBlockedAddress = errors.New("saml: idp metadata host is not a public address")

// flyPrivateNet is Fly.io's private 6PN range, which net.IP.IsPrivate does not
// cover (fdaa::/16 is inside fc00::/7 so it does, but keep it explicit).
var flyPrivateNet = mustCIDR("fdaa::/16")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err) // constant input; unreachable
	}
	return n
}

// isPublicIP reports whether ip is a routable public address.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsMulticast(), ip.IsUnspecified(), ip.IsInterfaceLocalMulticast():
		return false
	}
	if flyPrivateNet.Contains(ip) {
		return false
	}
	// Carrier-grade NAT 100.64.0.0/10.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xC0 == 64 {
		return false
	}
	return true
}

// guardedDialControl rejects the connection after DNS resolution if the
// concrete address being dialled is not public, which also defeats DNS
// rebinding because it checks the IP actually connected to.
func guardedDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("saml: parsing dial address: %w", err)
	}
	if !isPublicIP(net.ParseIP(host)) {
		return errBlockedAddress
	}
	return nil
}

// newMetadataClient builds the HTTP client for IdP metadata fetches: public
// addresses only, https-only redirects, bounded redirect depth.
func newMetadataClient() *http.Client {
	dialer := &net.Dialer{Timeout: metadataDialTimeout, Control: guardedDialControl}
	return &http.Client{
		Timeout:   metadataFetchTimeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= metadataMaxRedirects {
				return errors.New("saml: too many redirects fetching idp metadata")
			}
			if req.URL.Scheme != "https" {
				return errors.New("saml: idp metadata redirect must stay on https")
			}
			return nil
		},
	}
}
