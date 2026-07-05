package helpdocs

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"stonesuite-backend/ai"
)

// HelpStore is the write side this package depends on — satisfied by
// *ai.CPHelpStore. Defined here (point of use) so IngestFS is testable
// without a real database.
type HelpStore interface {
	ReplaceDoc(ctx context.Context, docKey string, chunks []ai.HelpChunk) error
}

// Result reports per-doc outcomes from IngestFS. A doc key appears in
// exactly one of Ingested or Failed. JSON tags keep the reindex-help HTTP
// response consistent with this codebase's lowercase-key convention (see
// AskResult, and the "enqueued" key in AIOps.Reindex's response).
type Result struct {
	Ingested []string          `json:"ingested"`
	Failed   map[string]string `json:"failed"` // doc key -> error message
}

// IngestFS chunks every *.md file in fsys by heading, embeds each section
// via embedder, and replaces that doc's chunks in store (keyed by the
// file's base name without extension). A per-file failure is recorded in
// Result.Failed and does not stop the remaining files — the caller (the
// rag-ingest-help CLI or the reindex-help HTTP handler) decides how to
// surface partial failure.
func IngestFS(ctx context.Context, embedder ai.Embedder, store HelpStore, fsys fs.FS) (Result, error) {
	names, err := fs.Glob(fsys, "*.md")
	if err != nil {
		return Result{}, fmt.Errorf("glob: %w", err)
	}
	res := Result{Failed: map[string]string{}}
	for _, name := range names {
		docKey := strings.TrimSuffix(name, filepath.Ext(name))
		if err := ingestOne(ctx, embedder, store, fsys, name, docKey); err != nil {
			res.Failed[docKey] = err.Error()
			continue
		}
		res.Ingested = append(res.Ingested, docKey)
	}
	return res, nil
}

// ingestOne chunks one file, embeds each section, and replaces its doc_key's
// chunks in store.
func ingestOne(ctx context.Context, embedder ai.Embedder, store HelpStore, fsys fs.FS, name, docKey string) error {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	sections := ChunkMarkdown(string(raw), docKey)
	if len(sections) == 0 {
		return fmt.Errorf("no content to chunk")
	}

	chunks := make([]ai.HelpChunk, 0, len(sections))
	for _, sec := range sections {
		vecs, err := embedder.Embed(ctx, []string{sec.Content})
		if err != nil {
			return fmt.Errorf("embed section %q: %w", sec.Title, err)
		}
		chunks = append(chunks, ai.HelpChunk{Section: sec.Title, Content: sec.Content, Embedding: vecs[0]})
	}
	if err := store.ReplaceDoc(ctx, docKey, chunks); err != nil {
		return fmt.Errorf("replace doc: %w", err)
	}
	return nil
}
