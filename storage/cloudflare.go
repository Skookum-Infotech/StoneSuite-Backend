package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const cfAPIBase = "https://api.cloudflare.com/client/v4"

// CFClientIface is the subset of CFClient used by handlers so callers can
// receive nil without a type assertion.
type CFClientIface interface {
	IsConfigured() bool
	CreateBucket(ctx context.Context, name string) error
	SetBucketCORS(ctx context.Context, bucket string, origins []string) error
}

// CFClient calls the Cloudflare management API (distinct from R2 S3-compat
// API) to provision per-tenant R2 buckets and set their CORS policies.
type CFClient struct {
	accountID string
	apiToken  string
	http      *http.Client
}

// NewCFClient builds a Cloudflare management API client. Returns nil when
// accountID or apiToken is blank — callers treat nil as "not configured."
func NewCFClient(accountID, apiToken string) *CFClient {
	if accountID == "" || apiToken == "" {
		return nil
	}
	return &CFClient{
		accountID: accountID,
		apiToken:  apiToken,
		http:      &http.Client{},
	}
}

// IsConfigured reports whether the client has valid credentials.
func (c *CFClient) IsConfigured() bool { return c != nil }

// BucketName returns the canonical per-tenant bucket name for a slug.
// Convention: ss-{slug}. The "ss-" prefix avoids collisions with manually
// created buckets and makes StoneSuite buckets easy to identify.
func BucketName(slug string) string { return "ss-" + slug }

// CreateBucket creates an R2 bucket in the account. Idempotent: if the bucket
// already exists Cloudflare returns a 409 which this function silently ignores.
func (c *CFClient) CreateBucket(ctx context.Context, name string) error {
	if c == nil {
		return fmt.Errorf("cloudflare client not configured")
	}
	body, _ := json.Marshal(map[string]string{"name": name})
	url := fmt.Sprintf("%s/accounts/%s/r2/buckets", cfAPIBase, c.accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build create-bucket request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("create r2 bucket: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	// 200 = created; 409 = already exists (idempotent).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("create r2 bucket: HTTP %d", resp.StatusCode)
	}
	return nil
}

// cfCORSAllowed is the nested allowed block in a Cloudflare R2 CORS rule.
type cfCORSAllowed struct {
	Origins []string `json:"origins"`
	Methods []string `json:"methods"`
	Headers []string `json:"headers"`
}

// cfCORSRule mirrors the shape the Cloudflare R2 management API expects.
// NOTE: this is NOT the S3 CORS format — Cloudflare uses a different schema.
type cfCORSRule struct {
	Allowed       cfCORSAllowed `json:"allowed"`
	MaxAgeSeconds int           `json:"maxAgeSeconds"`
}

// SetBucketCORS configures the CORS policy on a bucket so browsers can PUT
// files directly from the listed origins. Safe to call repeatedly (PUT = full
// replacement).
func (c *CFClient) SetBucketCORS(ctx context.Context, bucket string, origins []string) error {
	if c == nil {
		return fmt.Errorf("cloudflare client not configured")
	}
	rules := []cfCORSRule{{
		Allowed: cfCORSAllowed{
			Origins: origins,
			Methods: []string{"PUT", "GET", "DELETE"},
			Headers: []string{
				"Content-Type",
				"X-Amz-Date",
				"X-Amz-Algorithm",
				"X-Amz-Credential",
				"X-Amz-Signature",
				"X-Amz-Signed-Headers",
				"X-Amz-Expires",
			},
		},
		MaxAgeSeconds: 300,
	}}
	body, _ := json.Marshal(map[string]any{"rules": rules})
	url := fmt.Sprintf("%s/accounts/%s/r2/buckets/%s/cors", cfAPIBase, c.accountID, bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build set-cors request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("set r2 bucket cors: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("set r2 bucket cors: HTTP %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
