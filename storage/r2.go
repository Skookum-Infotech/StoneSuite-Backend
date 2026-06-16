// Package storage provides a Cloudflare R2 client (S3-compatible) for
// StoneSuite's record-attachment feature. It implements AWS Signature
// Version 4 presigning and authenticated DELETE requests using only the
// Go standard library — no aws-sdk-go required.
//
// The public surface is intentionally small:
//   - PresignPut: browser-uploadable PUT URL (TTL ~5 min)
//   - PresignGet: download URL with Content-Disposition:attachment (TTL ~60 s)
//   - Delete:     server-side object removal (best-effort)
package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"stonesuite-backend/config"
)

const (
	awsAlgorithm    = "AWS4-HMAC-SHA256"
	awsService      = "s3"
	awsRegion       = "auto" // Cloudflare R2 uses "auto" as the pseudo-region
	emptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// ErrStorageNotConfigured is returned when R2 credentials are absent.
// Attachment presign/download endpoints return HTTP 503 in this case;
// the binary still starts cleanly for development environments.
var ErrStorageNotConfigured = errors.New("R2 storage not configured (set R2_ACCOUNT_ID, R2_BUCKET, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY)")

// Client holds the R2 credentials and bucket name. A nil Client is valid
// and returns ErrStorageNotConfigured on every call — callers should treat
// nil the same way.
type Client struct {
	bucket    string
	accessKey string
	secretKey string
	host      string // {accountID}.r2.cloudflarestorage.com
}

// New builds a Client from the application config. Returns (nil, nil) when
// any required R2 field is blank — the binary should start normally and
// return 503 on attachment endpoints that require R2.
func New(cfg config.Config) (*Client, error) {
	if cfg.R2AccountID == "" || cfg.R2Bucket == "" ||
		cfg.R2AccessKeyID == "" || cfg.R2SecretAccessKey == "" {
		return nil, nil
	}
	return &Client{
		bucket:    cfg.R2Bucket,
		accessKey: cfg.R2AccessKeyID,
		secretKey: cfg.R2SecretAccessKey,
		host:      cfg.R2AccountID + ".r2.cloudflarestorage.com",
	}, nil
}

// IsConfigured reports whether the client has valid credentials.
func (c *Client) IsConfigured() bool { return c != nil }

// PresignPut returns a presigned PUT URL that the browser can use to upload
// directly to R2. TTL is typically 5 minutes.
func (c *Client) PresignPut(_ context.Context, key, _ string, ttl time.Duration) (string, error) {
	if c == nil {
		return "", ErrStorageNotConfigured
	}
	return c.presignURL("PUT", key, nil, ttl)
}

// PresignGet returns a presigned GET URL with response-content-disposition=attachment
// so browsers download the file rather than rendering it inline — important for
// uploaded HTML/SVG which could otherwise execute scripts in the origin context.
// TTL is typically 60 seconds.
func (c *Client) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	if c == nil {
		return "", ErrStorageNotConfigured
	}
	extra := url.Values{}
	extra.Set("response-content-disposition", "attachment")
	return c.presignURL("GET", key, extra, ttl)
}

// Delete removes an object from R2. Returns nil when the object does not
// exist (idempotent, matches S3 semantics). Errors are logged by the caller
// and the metadata row is removed regardless (best-effort semantics).
func (c *Client) Delete(ctx context.Context, key string) error {
	if c == nil {
		return ErrStorageNotConfigured
	}
	return c.signedDelete(ctx, key)
}

// ---- presigning (AWS SigV4 query-parameter auth) ----------------------------

// presignURL constructs a presigned AWS SigV4 URL using path-style R2 access.
// Path style: https://{accountID}.r2.cloudflarestorage.com/{bucket}/{key}
func (c *Client) presignURL(method, key string, extraQuery url.Values, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	credScope := dateStamp + "/" + awsRegion + "/" + awsService + "/aws4_request"

	// Assemble all query params that will appear in the presigned URL.
	q := url.Values{}
	for k, vs := range extraQuery {
		q[k] = vs
	}
	q.Set("X-Amz-Algorithm", awsAlgorithm)
	q.Set("X-Amz-Credential", c.accessKey+"/"+credScope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(ttl.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")

	// Canonical request.
	canonURI := "/" + awsEncodeSegment(c.bucket) + "/" + encodeKeyPath(key)
	canonQS := canonicalQueryString(q)
	canonHeaders := "host:" + c.host + "\n"

	canonReq := strings.Join([]string{
		method, canonURI, canonQS, canonHeaders, "host", "UNSIGNED-PAYLOAD",
	}, "\n")

	// String to sign.
	s2s := strings.Join([]string{
		awsAlgorithm, amzDate, credScope, hexSHA256([]byte(canonReq)),
	}, "\n")

	sig := hexHMAC(signingKey(c.secretKey, dateStamp, awsRegion, awsService), []byte(s2s))
	q.Set("X-Amz-Signature", sig)

	// Build the final URL. The canonical query string is used here too so
	// the encoding in the URL exactly matches what was signed.
	rawURL := "https://" + c.host + "/" + c.bucket + "/" + encodeKeyPath(key)
	return rawURL + "?" + canonicalQueryString(q), nil
}

// ---- authenticated DELETE (AWS SigV4 auth-header) ---------------------------

func (c *Client) signedDelete(ctx context.Context, key string) error {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	credScope := dateStamp + "/" + awsRegion + "/" + awsService + "/aws4_request"
	signedHdrs := "host;x-amz-content-sha256;x-amz-date"

	canonURI := "/" + awsEncodeSegment(c.bucket) + "/" + encodeKeyPath(key)
	canonHeaders := "host:" + c.host + "\n" +
		"x-amz-content-sha256:" + emptyBodySHA256 + "\n" +
		"x-amz-date:" + amzDate + "\n"

	canonReq := strings.Join([]string{
		"DELETE", canonURI, "", canonHeaders, signedHdrs, emptyBodySHA256,
	}, "\n")

	s2s := strings.Join([]string{
		awsAlgorithm, amzDate, credScope, hexSHA256([]byte(canonReq)),
	}, "\n")

	sig := hexHMAC(signingKey(c.secretKey, dateStamp, awsRegion, awsService), []byte(s2s))

	authHeader := fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		awsAlgorithm, c.accessKey, credScope, signedHdrs, sig,
	)

	objURL := "https://" + c.host + "/" + c.bucket + "/" + encodeKeyPath(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, objURL, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	req.Header.Set("Host", c.host)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", emptyBodySHA256)
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute r2 delete: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 204 No Content = deleted; 404 = already gone (idempotent).
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("r2 delete returned HTTP %d", resp.StatusCode)
}

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
