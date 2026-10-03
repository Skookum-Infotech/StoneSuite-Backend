package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotFound is returned by Head and GetLimited when the object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// ErrTooLarge is returned by GetLimited when the object body exceeds maxBytes.
var ErrTooLarge = errors.New("storage: object exceeds size limit")

// ObjectInfo is the metadata Head returns for an object.
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// newSignedRequest builds a SigV4-signed, empty-body request for key.
func (c *Client) newSignedRequest(ctx context.Context, method, key string) (*http.Request, error) {
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
		method, canonURI, "", canonHeaders, signedHdrs, emptyBodySHA256,
	}, "\n")
	s2s := strings.Join([]string{
		awsAlgorithm, amzDate, credScope, hexSHA256([]byte(canonReq)),
	}, "\n")
	sig := hexHMAC(signingKey(c.secretKey, dateStamp, awsRegion, awsService), []byte(s2s))
	authHeader := fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		awsAlgorithm, c.accessKey, credScope, signedHdrs, sig,
	)

	objURL := c.baseURL() + "/" + c.bucket + "/" + encodeKeyPath(key)
	req, err := http.NewRequestWithContext(ctx, method, objURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build r2 %s request: %w", strings.ToLower(method), err)
	}
	req.Header.Set("Host", c.host)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", emptyBodySHA256)
	req.Header.Set("Authorization", authHeader)
	return req, nil
}

// Head returns an object's size and content type via a SigV4 HEAD request,
// or ErrNotFound when the object does not exist.
func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if c == nil {
		return ObjectInfo{}, ErrStorageNotConfigured
	}
	req, err := c.newSignedRequest(ctx, http.MethodHead, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("execute r2 head: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return ObjectInfo{Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}, nil
	case http.StatusNotFound:
		return ObjectInfo{}, ErrNotFound
	default:
		return ObjectInfo{}, fmt.Errorf("r2 head returned HTTP %d", resp.StatusCode)
	}
}

// GetLimited downloads an object's body, reading at most maxBytes. It returns
// ErrNotFound on 404 and ErrTooLarge when the body exceeds maxBytes.
func (c *Client) GetLimited(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if c == nil {
		return nil, ErrStorageNotConfigured
	}
	req, err := c.newSignedRequest(ctx, http.MethodGet, key)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute r2 get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("r2 get returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read r2 object body: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}
