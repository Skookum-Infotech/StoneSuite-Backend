package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestNew_R2EndpointOverride(t *testing.T) {
	base := config.Config{CloudflareAccountID: "acct", R2AccessKeyID: "k", R2SecretAccessKey: "s"}
	tests := []struct {
		name       string
		env        string
		endpoint   string
		wantPrefix string
		wantErr    bool
	}{
		{"no override uses real R2", "development", "", "https://acct.r2.cloudflarestorage.com/b/", false},
		{"development honours override", "development", "http://localhost:9900", "http://localhost:9900/b/", false},
		{"production ignores override", "production", "http://localhost:9900", "https://acct.r2.cloudflarestorage.com/b/", false},
		{"staging ignores override", "staging", "http://evil.example", "https://acct.r2.cloudflarestorage.com/b/", false},
		{"bad scheme rejected", "development", "ftp://localhost:9900", "", true},
		{"missing host rejected", "development", "localhost", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.Environment, cfg.R2Endpoint = tt.env, tt.endpoint
			c, err := New(cfg)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			u, err := c.WithBucket("b").PresignPut(context.Background(), "x.pdf", "application/pdf", time.Minute)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(u, tt.wantPrefix), u)
		})
	}
}
