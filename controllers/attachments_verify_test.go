package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/storage"
	"stonesuite-backend/workflow"
)

func TestAttachmentKeyForRecord(t *testing.T) {
	prefix := workflow.StorageKeyPrefix("acme", "lead", "rec-1")
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"key presign minted for this record", workflow.GenerateStorageKey("acme", "lead", "rec-1", "uuid", "a.pdf"), true},
		{"another record of the same tenant", workflow.GenerateStorageKey("acme", "lead", "rec-2", "uuid", "a.pdf"), false},
		{"record id that merely shares a prefix", "acme/lead/rec-10/uuid.pdf", false},
		{"an import upload", "acme/imports/uuid.csv", false},
		{"another tenant", "other/lead/rec-1/uuid.pdf", false},
		{"nested path under the record", "acme/lead/rec-1/sub/uuid.pdf", false},
		{"traversal", "acme/lead/rec-1/..", false},
		{"bare prefix", prefix, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, attachmentKeyForRecord(tt.key, prefix))
		})
	}
}

// fakeHeader answers Head from a map; a missing key is storage.ErrNotFound.
type fakeHeader struct {
	objects map[string]storage.ObjectInfo
	err     error
}

func (f fakeHeader) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	if f.err != nil {
		return storage.ObjectInfo{}, f.err
	}
	info, ok := f.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return info, nil
}

func TestVerifyUploadedObjects(t *testing.T) {
	store := fakeHeader{objects: map[string]storage.ObjectInfo{
		"k1": {Size: 10, ContentType: "application/pdf"},
		"k2": {Size: 20, ContentType: "image/png"},
	}}

	t.Run("returns what storage holds, in order", func(t *testing.T) {
		got, err := verifyUploadedObjects(context.Background(), store, []string{"k2", "k1"})
		require.NoError(t, err)
		assert.Equal(t, []storage.ObjectInfo{store.objects["k2"], store.objects["k1"]}, got)
	})
	t.Run("a missing object is not-uploaded", func(t *testing.T) {
		_, err := verifyUploadedObjects(context.Background(), store, []string{"k1", "missing"})
		require.ErrorIs(t, err, errAttachmentNotUploaded)
	})
	t.Run("a storage failure is not mistaken for a missing upload", func(t *testing.T) {
		_, err := verifyUploadedObjects(context.Background(), fakeHeader{err: errors.New("boom")}, []string{"k1"})
		require.Error(t, err)
		assert.NotErrorIs(t, err, errAttachmentNotUploaded)
	})
}

func TestStoredContentType(t *testing.T) {
	tests := []struct {
		name, stored, declared, want string
	}{
		{"stored wins", "application/pdf", "image/png", "application/pdf"},
		{"parameters dropped", "image/png; charset=binary", "", "image/png"},
		{"falls back to declared", "", "image/png", "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, storedContentType(storage.ObjectInfo{ContentType: tt.stored}, tt.declared))
		})
	}
}

func TestStoredKeyOwnedByRecord(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"this record", "acme/lead/rec-1/uuid.pdf", true},
		{"this record under its pre-conversion workflow", "acme/prospect/rec-1/uuid.pdf", true},
		{"another record", "acme/lead/rec-2/uuid.pdf", false},
		{"an import", "acme/imports/uuid.csv", false},
		{"feedback", "acme/feedback/fb-1/uuid.png", false},
		{"another tenant", "other/lead/rec-1/uuid.pdf", false},
		{"traversal", "acme/../rec-1/uuid.pdf", false},
		{"too deep", "acme/lead/rec-1/x/uuid.pdf", false},
		{"empty file", "acme/lead/rec-1/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, storedKeyOwnedByRecord(tt.key, "acme", "rec-1"))
		})
	}
}
