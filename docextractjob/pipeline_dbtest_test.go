//go:build dbtest

package docextractjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
	"stonesuite-backend/storage"
)

// fakeObjects is an in-memory objectStore.
type fakeObjects struct {
	data map[string][]byte
	gets int
}

func (f *fakeObjects) GetLimited(_ context.Context, key string, _ int64) ([]byte, error) {
	f.gets++
	b, ok := f.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return b, nil
}

func (f *fakeObjects) Delete(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}

func queuedExtraction(t *testing.T, s *Store, clientSHA string) *Extraction {
	t.Helper()
	e := newExtraction(t, s, ownerA)
	_, _, err := s.MarkQueued(context.Background(), e.ID, ownerA, clientSHA, "job")
	require.NoError(t, err)
	return e
}

func runnerFor(t *testing.T, objs *fakeObjects) (*jobRunner, *Store) {
	pool := testPool(t)
	s := NewStore(pool)
	return &jobRunner{store: s, pool: pool, objects: objs, cfg: Config{StagingTTL: 24 * time.Hour}, tenantID: "t1"}, s
}

func TestPipeline_FailureModes(t *testing.T) {
	ctx := context.Background()
	garbage := []byte("this is not a pdf or a docx")

	t.Run("object missing is dead upload_missing", func(t *testing.T) {
		r, s := runnerFor(t, &fakeObjects{data: map[string][]byte{}})
		e := queuedExtraction(t, s, "")
		out := r.process(ctx, e.ID)
		assert.Equal(t, outcomeDead, out.kind)
		assert.Equal(t, FailUploadMissing, out.code)
	})
	t.Run("client sha mismatch is dead upload_corrupted", func(t *testing.T) {
		objs := &fakeObjects{data: map[string][]byte{}}
		r, s := runnerFor(t, objs)
		e := queuedExtraction(t, s, "0000")
		objs.data[e.StagingKey] = garbage
		out := r.process(ctx, e.ID)
		assert.Equal(t, outcomeDead, out.kind)
		assert.Equal(t, FailUploadCorrupted, out.code)
	})
	t.Run("wrong magic is dead unsupported_type", func(t *testing.T) {
		objs := &fakeObjects{data: map[string][]byte{}}
		r, s := runnerFor(t, objs)
		sum := sha256.Sum256(garbage)
		e := queuedExtraction(t, s, hex.EncodeToString(sum[:]))
		objs.data[e.StagingKey] = garbage
		out := r.process(ctx, e.ID)
		assert.Equal(t, outcomeDead, out.kind)
		assert.Equal(t, string(docextract.FailUnsupportedType), out.code)
	})
	t.Run("discarded extraction is skipped", func(t *testing.T) {
		r, s := runnerFor(t, &fakeObjects{data: map[string][]byte{}})
		e := queuedExtraction(t, s, "")
		_, err := s.Discard(ctx, e.ID, ownerA)
		require.NoError(t, err)
		assert.Equal(t, outcomeSkipped, r.process(ctx, e.ID).kind)
	})
	t.Run("missing extraction row is skipped", func(t *testing.T) {
		r, _ := runnerFor(t, &fakeObjects{data: map[string][]byte{}})
		assert.Equal(t, outcomeSkipped, r.process(ctx, "55555555-5555-4555-8555-555555555555").kind)
	})
}

func TestPipeline_CacheHitCopiesResultAndFlagsSameFile(t *testing.T) {
	ctx := context.Background()
	objs := &fakeObjects{data: map[string][]byte{}}
	r, s := runnerFor(t, objs)
	body := []byte("cached bytes, never parsed")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	// An earlier extraction of the same file, ready with a result.
	first := queuedExtraction(t, s, "")
	require.NoError(t, s.MarkRunning(ctx, first.ID))
	cached := ResultDoc{Extracted: docextract.Result{DocType: docextract.DocTypeSalesOrder,
		Header: docextract.Header{PONumber: docextract.Field{Value: "PO-CACHED"}}}}
	require.NoError(t, s.SaveResult(ctx, first.ID, SaveResultParams{SHA256: sha, Method: docextract.MethodParser, Result: cached}))
	// The earlier file produced a record: only then is a re-upload a duplicate.
	require.NoError(t, s.CompleteCAS(ctx, first.ID, ownerA, "11111111-2222-4333-8444-555555555555"))

	second := queuedExtraction(t, s, sha)
	objs.data[second.StagingKey] = body
	out := r.process(ctx, second.ID)
	require.Equal(t, outcomeSuccess, out.kind, "err=%v", out.err)
	assert.Equal(t, MethodCache, out.method)

	got, err := s.Get(ctx, second.ID, ownerA)
	require.NoError(t, err)
	assert.Equal(t, StatusReady, got.Status)
	assert.Equal(t, docextract.MethodParser, got.Method, "stored method is unchanged by a cache hit")
	var doc ResultDoc
	require.NoError(t, json.Unmarshal(got.Result, &doc))
	assert.Equal(t, "PO-CACHED", doc.Extracted.Header.PONumber.Value)
	require.Len(t, doc.Duplicates, 1)
	assert.Equal(t, DupSameFile, doc.Duplicates[0].Kind)
}
