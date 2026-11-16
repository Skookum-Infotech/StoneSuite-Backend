package usage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContextHelpers(t *testing.T) {
	tests := []struct {
		name       string
		ctx        context.Context
		wantTenant string
		wantSource Source
	}{
		{"empty", context.Background(), "", SourceOther},
		{"explicit tenant and source", WithSource(WithTenant(context.Background(), "t1"), SourceImport), "t1", SourceImport},
		{"blank tenant ignored", WithTenant(context.Background(), ""), "", SourceOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantTenant, TenantFrom(tc.ctx))
			assert.Equal(t, tc.wantSource, SourceFrom(tc.ctx))
		})
	}
}
