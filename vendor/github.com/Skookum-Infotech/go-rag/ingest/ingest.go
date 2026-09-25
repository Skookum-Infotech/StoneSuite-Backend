package ingest

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Skookum-Infotech/go-rag/chunk"
	"github.com/Skookum-Infotech/go-rag/rag"
)

// HelpStore is the write side this package depends on — satisfied by
// *ai.CPHelpStore. Defined here (point of use) so IngestFS is testable
// without a real database.
type HelpStore interface {
	ReplaceDoc(ctx context.Context, docKey string, chunks []DocChunk) error
}

// Result reports per-doc outcomes from IngestFS. A doc key appears in
// exactly one of Ingested or Failed. JSON tags keep the reindex-help HTTP
// response consistent with this codebase's lowercase-key convention (see
// AskResult, and the "enqueued" key in AIOps.Reindex's response).
type Result struct {
	Ingested []string          `json:"ingested"`
	Failed   map[string]string `json:"failed"` // doc key -> error message
}

// DefaultChunkOpts caps each embedded piece safely under the ~512-token
// context window documented for AI_EMBED_MODEL (docs/ai-assistant.md),
// leaving headroom for the embedder's task prefix (see provider/ollama).
// Overlap keeps a fact stated near a chunk boundary from disappearing
// entirely from one side of the split.
var DefaultChunkOpts = chunk.Options{MaxTokens: 400, Overlap: 40}

// IngestFS chunks every *.md file in fsys by heading, sub-splits any
// heading section that exceeds chunkOpts (see defect: a heading-only split
// let long sections truncate silently at the embedder's context window,
// with the tail never indexed), embeds every chunk via embedder, and
// replaces that doc's chunks in store (keyed by the file's base name
// without extension). A per-file failure is recorded in Result.Failed and
// does not stop the remaining files — the caller (the rag-ingest-help CLI
// or the reindex-help HTTP handler) decides how to surface partial failure.
func IngestFS(ctx context.Context, embedder rag.Embedder, store HelpStore, fsys fs.FS, chunkOpts chunk.Options) (Result, error) {
	names, err := fs.Glob(fsys, "*.md")
	if err != nil {
		return Result{}, fmt.Errorf("glob: %w", err)
	}
	res := Result{Failed: map[string]string{}}
	for _, name := range names {
		docKey := strings.TrimSuffix(name, filepath.Ext(name))
		if err := ingestOne(ctx, embedder, store, fsys, name, docKey, chunkOpts); err != nil {
			res.Failed[docKey] = err.Error()
			continue
		}
		res.Ingested = append(res.Ingested, docKey)
	}
	return res, nil
}

// ingestOne chunks one file by heading, sub-splits oversized sections,
// embeds every resulting chunk in one batched call, and replaces the
// doc_key's chunks in store.
func ingestOne(ctx context.Context, embedder rag.Embedder, store HelpStore, fsys fs.FS, name, docKey string, chunkOpts chunk.Options) error {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	sections := ChunkMarkdown(string(raw), docKey)
	if len(sections) == 0 {
		return fmt.Errorf("no content to chunk")
	}

	labels, texts := splitSections(sections, chunkOpts)
	if len(texts) == 0 {
		return fmt.Errorf("no content to chunk")
	}

	// One batched embed call for the whole document rather than one per
	// chunk — provider.Embed splits internally at its own HTTP batch cap.
	vecs, err := embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed %s: %w", docKey, err)
	}

	chunks := make([]DocChunk, len(texts))
	for i := range texts {
		chunks[i] = DocChunk{Section: labels[i], Content: texts[i], Embedding: vecs[i]}
	}
	if err := store.ReplaceDoc(ctx, docKey, chunks); err != nil {
		return fmt.Errorf("replace doc: %w", err)
	}
	return nil
}

// splitSections sub-splits any Section whose content exceeds chunkOpts's
// token budget, returning parallel label/text slices ready to embed. A
// section that fits becomes one chunk labeled with its heading title
// unchanged; a section that doesn't becomes several, labeled "Title (i/n)"
// so each keeps a distinct citation identity (HelpCorpus cites by section
// label — see ai.HelpCorpus in StoneSuite).
//
// Two headings with the same title in one doc (two "Overview"s) would share a
// citation identity and collapse into one during fusion, silently dropping a
// section; the repeat is labeled "Title #2" instead.
func splitSections(sections []Section, chunkOpts chunk.Options) (labels, texts []string) {
	seen := map[string]int{}
	for _, sec := range sections {
		title := sec.Title
		seen[title]++
		if n := seen[title]; n > 1 {
			title = fmt.Sprintf("%s #%d", title, n)
		}
		parts := chunk.Split(sec.Content, chunkOpts)
		if len(parts) <= 1 {
			labels = append(labels, title)
			texts = append(texts, sec.Content)
			continue
		}
		for i, part := range parts {
			labels = append(labels, fmt.Sprintf("%s (%d/%d)", title, i+1, len(parts)))
			texts = append(texts, part)
		}
	}
	return labels, texts
}

// DocPruner is the optional delete side of a HelpStore: it removes every doc
// whose key is NOT in keep, returning how many were removed.
type DocPruner interface {
	PruneDocs(ctx context.Context, keep []string) (int, error)
}

// PruneMissing deletes docs from store that no longer exist as *.md files in
// fsys — a doc removed from the corpus otherwise keeps answering questions
// forever. Separate from IngestFS on purpose: pruning is only correct when
// fsys is the COMPLETE corpus, and a caller ingesting a subset must not
// silently delete everything else.
func PruneMissing(ctx context.Context, store DocPruner, fsys fs.FS) (int, error) {
	names, err := fs.Glob(fsys, "*.md")
	if err != nil {
		return 0, fmt.Errorf("glob: %w", err)
	}
	if len(names) == 0 {
		// An empty corpus is far more likely a wiring mistake than an intent
		// to delete every doc.
		return 0, fmt.Errorf("prune: corpus has no docs; refusing to delete everything")
	}
	keep := make([]string, len(names))
	for i, name := range names {
		keep[i] = strings.TrimSuffix(name, filepath.Ext(name))
	}
	n, err := store.PruneDocs(ctx, keep)
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	return n, nil
}

// DocChunk is one embeddable section of a document, ready for a HelpStore to
// persist. Section is the heading it came from — used as the citation's
// SourceID, so it is what a reader sees when the assistant cites this text.
type DocChunk struct {
	Section   string
	Content   string
	Embedding []float32
}
