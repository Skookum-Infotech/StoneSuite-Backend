package docextractjob

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docextract"
	"stonesuite-backend/storage"
)

func TestClassifyExtractError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind string
		wantCode string
	}{
		{"input error is dead", &docextract.InputError{Code: docextract.FailScanned}, outcomeDead, "scanned"},
		{"wrapped input error is dead", fmt.Errorf("wrap: %w", &docextract.InputError{Code: docextract.FailPageCap}), outcomeDead, "page_cap"},
		{"cancellation retries", context.DeadlineExceeded, outcomeRetry, FailTransient},
		{"plain error retries", errors.New("boom"), outcomeRetry, FailTransient},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyExtractError(tc.err)
			assert.Equal(t, tc.wantKind, got.kind)
			assert.Equal(t, tc.wantCode, got.code)
		})
	}
}

func TestClassifyDownloadError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind string
		wantCode string
	}{
		{"missing object", fmt.Errorf("fetch: %w", storage.ErrNotFound), outcomeDead, FailUploadMissing},
		{"too large", storage.ErrTooLarge, outcomeDead, "too_large"},
		{"r2 5xx retries", errors.New("r2 get returned HTTP 503"), outcomeRetry, FailTransient},
		{"not configured retries", storage.ErrStorageNotConfigured, outcomeRetry, FailTransient},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDownloadError(tc.err)
			assert.Equal(t, tc.wantKind, got.kind)
			assert.Equal(t, tc.wantCode, got.code)
		})
	}
}
