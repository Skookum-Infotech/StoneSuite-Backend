package storage

import (
	"context"
	"errors"
)

// BucketUsage is the total size and object count of one bucket.
type BucketUsage struct {
	Bytes   int64
	Objects int64
}

// Usage pages through every object in the client's bucket (ListObjectsV2) and
// returns their summed size and count. It is read-only and measures only the
// bucket selected with WithBucket — the tenant's own bucket — never any other.
func (c *Client) Usage(ctx context.Context) (BucketUsage, error) {
	if c == nil {
		return BucketUsage{}, ErrStorageNotConfigured
	}
	if c.bucket == "" {
		return BucketUsage{}, errors.New("r2 usage: no bucket selected (use WithBucket)")
	}
	var total BucketUsage
	token := ""
	for {
		page, err := c.listPage(ctx, token)
		if err != nil {
			return total, err
		}
		total.Bytes += page.bytes
		total.Objects += int64(len(page.keys))
		if !page.truncated || page.nextToken == "" {
			return total, nil
		}
		token = page.nextToken
	}
}
