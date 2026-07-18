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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
var ErrStorageNotConfigured = errors.New("R2 storage not configured (set CLOUDFLARE_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY)")

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
// any required credential is blank — the binary starts normally and returns
// 503 on attachment endpoints. No default bucket is set; callers must use
// WithBucket to address the per-tenant bucket on every request.
func New(cfg config.Config) (*Client, error) {
	if cfg.CloudflareAccountID == "" || cfg.R2AccessKeyID == "" || cfg.R2SecretAccessKey == "" {
		return nil, nil
	}
	return &Client{
		accessKey: cfg.R2AccessKeyID,
		secretKey: cfg.R2SecretAccessKey,
		host:      cfg.CloudflareAccountID + ".r2.cloudflarestorage.com",
	}, nil
}

// IsConfigured reports whether the client has valid credentials.
func (c *Client) IsConfigured() bool { return c != nil }

// WithBucket returns a shallow copy of the client pointed at the given tenant
// bucket. Returns nil when either the client or bucket is nil/empty — a nil
// return is treated by callers as "not configured" and surfaces as HTTP 503,
// preventing silent fallback to a wrong bucket.
func (c *Client) WithBucket(bucket string) *Client {
	if c == nil || bucket == "" {
		return nil
	}
	cp := *c
	cp.bucket = bucket
	return &cp
}

// PresignPut returns a presigned PUT URL that the browser can use to upload
// directly to R2. The contentType is included in the signed headers so R2
// validates that the browser uploads exactly the declared MIME type.
// TTL is typically 5 minutes.
func (c *Client) PresignPut(_ context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if c == nil {
		return "", ErrStorageNotConfigured
	}
	return c.presignURL("PUT", key, contentType, nil, ttl)
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
	return c.presignURL("GET", key, "", extra, ttl)
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
// contentType, when non-empty, is added to the signed headers so R2 validates
// that the browser uploads the declared MIME type (PUT only). For GET pass "".
func (c *Client) presignURL(method, key, contentType string, extraQuery url.Values, ttl time.Duration) (string, error) {
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

	// Include Content-Type in signed headers when provided (PUT uploads).
	// This binds the presigned URL to the exact MIME type declared during presigning,
	// preventing a browser from uploading a different file type with the same URL.
	var signedHdrNames string
	var canonHeaders string
	if contentType != "" {
		signedHdrNames = "content-type;host"
		canonHeaders = "content-type:" + contentType + "\n" + "host:" + c.host + "\n"
	} else {
		signedHdrNames = "host"
		canonHeaders = "host:" + c.host + "\n"
	}
	q.Set("X-Amz-SignedHeaders", signedHdrNames)

	// Canonical request.
	canonURI := "/" + awsEncodeSegment(c.bucket) + "/" + encodeKeyPath(key)
	canonQS := canonicalQueryString(q)

	canonReq := strings.Join([]string{
		method, canonURI, canonQS, canonHeaders, signedHdrNames, "UNSIGNED-PAYLOAD",
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
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 204 No Content = deleted; 404 = already gone (idempotent).
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("r2 delete returned HTTP %d", resp.StatusCode)
}
