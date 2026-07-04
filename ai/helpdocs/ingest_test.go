package helpdocs

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"stonesuite-backend/ai"
)

type fakeHelpStore struct {
	docs   map[string][]ai.HelpChunk
	failOn map[string]error
}

func (f *fakeHelpStore) ReplaceDoc(_ context.Context, docKey string, chunks []ai.HelpChunk) error {
	if err, ok := f.failOn[docKey]; ok {
		return err
	}
	if f.docs == nil {
		f.docs = map[string][]ai.HelpChunk{}
	}
	f.docs[docKey] = chunks
	return nil
}

func TestIngestFS_EmbedsAndReplacesEachDoc(t *testing.T) {
	fsys := fstest.MapFS{
		"getting-started.md": &fstest.MapFile{Data: []byte("# Getting Started\nCreate a lead from CRM > Leads > New.\n")},
	}
	store := &fakeHelpStore{}
	embedder := &ai.FakeEmbedder{Dim: 4}

	res, err := IngestFS(context.Background(), embedder, store, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 1 || res.Ingested[0] != "getting-started" {
		t.Fatalf("want ingested=[getting-started], got %+v", res.Ingested)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("want no failures, got %+v", res.Failed)
	}
	chunks := store.docs["getting-started"]
	if len(chunks) != 1 || chunks[0].Section != "Getting Started" {
		t.Fatalf("store did not receive the expected chunk, got %+v", chunks)
	}
}

func TestIngestFS_OneFileFailingDoesNotStopOthers(t *testing.T) {
	fsys := fstest.MapFS{
		"good.md": &fstest.MapFile{Data: []byte("# Good\nFine content.\n")},
		"bad.md":  &fstest.MapFile{Data: []byte("# Bad\nWill fail to store.\n")},
	}
	store := &fakeHelpStore{failOn: map[string]error{"bad": errors.New("db down")}}
	embedder := &ai.FakeEmbedder{Dim: 4}

	res, err := IngestFS(context.Background(), embedder, store, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 1 || res.Ingested[0] != "good" {
		t.Fatalf("want ingested=[good], got %+v", res.Ingested)
	}
	if msg, ok := res.Failed["bad"]; !ok || msg == "" {
		t.Fatalf("want a failure recorded for bad, got %+v", res.Failed)
	}
}

func TestIngestFS_EmptyFSProducesEmptyResult(t *testing.T) {
	res, err := IngestFS(context.Background(), &ai.FakeEmbedder{Dim: 4}, &fakeHelpStore{}, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 0 || len(res.Failed) != 0 {
		t.Fatalf("want empty result, got %+v", res)
	}
}

func TestIngestFS_EmbedFailureIsRecordedPerDoc(t *testing.T) {
	fsys := fstest.MapFS{
		"doc.md": &fstest.MapFile{Data: []byte("# Doc\nContent.\n")},
	}
	embedder := &ai.FakeEmbedder{Err: errors.New("ollama unreachable")}

	res, err := IngestFS(context.Background(), embedder, &fakeHelpStore{}, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ingested) != 0 {
		t.Fatalf("want no successes, got %+v", res.Ingested)
	}
	if msg, ok := res.Failed["doc"]; !ok || msg == "" {
		t.Fatalf("want a failure recorded for doc, got %+v", res.Failed)
	}
}
