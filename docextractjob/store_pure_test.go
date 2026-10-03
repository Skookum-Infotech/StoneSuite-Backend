package docextractjob

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStagingKey(t *testing.T) {
	assert.Equal(t, "acme/_staging/doc-extract/abc.pdf", StagingKey("acme", "abc", "pdf"))
}

func TestNewID(t *testing.T) {
	a, err := newID()
	require.NoError(t, err)
	b, err := newID()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.True(t, validUUID(a))
	assert.Equal(t, byte('4'), a[14], "version nibble")
}

func TestValidUUID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"canonical", "123e4567-e89b-42d3-a456-426614174000", true},
		{"upper", "123E4567-E89B-42D3-A456-426614174000", true},
		{"empty", "", false},
		{"short", "123e4567", false},
		{"injection", "x'; DROP TABLE document_extractions;--", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, validUUID(tc.in)) })
	}
}
