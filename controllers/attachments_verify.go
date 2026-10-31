package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"stonesuite-backend/storage"
)

// errAttachmentNotUploaded marks a confirm for an object that isn't in R2
// (the upload failed or never happened).
var errAttachmentNotUploaded = errors.New("attachment not uploaded")

// attachmentKeyForRecord reports whether key is a key presign would have
// minted for this record: the record's exact prefix plus one path segment.
func attachmentKeyForRecord(key, prefix string) bool {
	rest, ok := strings.CutPrefix(key, prefix)
	return ok && rest != "" && !strings.Contains(rest, "/") && !strings.Contains(rest, "..")
}

// objectHeader is the subset of *storage.Client confirm needs.
type objectHeader interface {
	Head(ctx context.Context, key string) (storage.ObjectInfo, error)
}

// verifyUploadedObjects HEADs each key and returns what R2 actually holds.
// The client's declared size and type are never trusted: they feed the
// per-record storage quota and the download's Content-Type.
func verifyUploadedObjects(ctx context.Context, store objectHeader, keys []string) ([]storage.ObjectInfo, error) {
	infos := make([]storage.ObjectInfo, 0, len(keys))
	for i, key := range keys {
		info, err := store.Head(ctx, key)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("attachment %d: %w", i+1, errAttachmentNotUploaded)
		}
		if err != nil {
			return nil, fmt.Errorf("attachment %d: head object: %w", i+1, err)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// failObjectVerify maps a verifyUploadedObjects error to a response.
func failObjectVerify(w http.ResponseWriter, err error) {
	if errors.Is(err, errAttachmentNotUploaded) {
		fail(w, http.StatusConflict, "A file was not uploaded. Please upload it again.")
		return
	}
	fail(w, http.StatusBadGateway, "Could not verify the uploaded files. Please try again.")
}

// storedContentType is the media type R2 recorded at upload (presign signs
// it, so it is what the client actually sent), falling back to the declared
// one if R2 returned none.
func storedContentType(obj storage.ObjectInfo, declared string) string {
	ct, _, _ := strings.Cut(obj.ContentType, ";")
	if ct = strings.TrimSpace(ct); ct != "" {
		return ct
	}
	return declared
}

// storedKeyOwnedByRecord guards download and delete of an existing row: the
// key must be tenantSlug/<workflow>/<recordID>/<file>. Rows confirmed before
// confirm checked the record prefix may point at another record's, an
// import's or a feedback object; serving or deleting those would cross the
// record's ownership scope. The workflow segment is not pinned because a CRM
// record keeps its id but changes workflow key when it converts.
func storedKeyOwnedByRecord(key, tenantSlug, recordID string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != storedKeySegments || parts[0] != tenantSlug || parts[2] != recordID {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// storedKeySegments is the segment count of an attachment key (see
// workflow.GenerateStorageKey): tenant, workflow, record, file.
const storedKeySegments = 4
