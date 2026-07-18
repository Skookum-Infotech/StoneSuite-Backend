package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ---- SigV4 crypto helpers ---------------------------------------------------

func hmacSHA256bytes(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hexHMAC(key, data []byte) string {
	return hex.EncodeToString(hmacSHA256bytes(key, data))
}

// signingKey derives the SigV4 signing key via the standard HMAC chain.
func signingKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256bytes([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256bytes(kDate, []byte(region))
	kService := hmacSHA256bytes(kRegion, []byte(service))
	return hmacSHA256bytes(kService, []byte("aws4_request"))
}

// ---- URI encoding helpers ---------------------------------------------------

// awsEncodeSegment percent-encodes a single path segment (no slashes) using
// AWS URI encoding rules (uppercase %XX, only unreserved chars pass through).
func awsEncodeSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// encodeKeyPath encodes an S3 object key segment-by-segment, preserving the
// '/' separators between segments (as required by the AWS canonical URI spec).
func encodeKeyPath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = awsEncodeSegment(p)
	}
	return strings.Join(parts, "/")
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~'
}

// canonicalQueryString builds the AWS canonical query string: keys and values
// are URI-encoded (uppercase %XX), sorted lexicographically by key then value,
// joined with '=' and '&'. This is used both for signing and as the literal
// query string in the presigned URL (so encoding is consistent).
func canonicalQueryString(q url.Values) string {
	type kv struct{ k, v string }
	var pairs []kv
	for key, vals := range q {
		ek := awsEncodeSegment(key)
		for _, val := range vals {
			pairs = append(pairs, kv{ek, awsEncodeSegment(val)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}
