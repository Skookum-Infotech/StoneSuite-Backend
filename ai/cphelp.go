package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Skookum-Infotech/go-rag/ingest"
	"github.com/Skookum-Infotech/go-rag/rag"
)

// CPHelpStore is the INGESTION path for app-help chunks in the control-plane
// pool's cp_rag_chunks table. Retrieval goes through HelpCorpus instead.
//
// The content is identical for every tenant and is nobody's private data,
// which is why it lives in the control plane rather than being duplicated per
// tenant, and why its corpus carries no scope filter.
type CPHelpStore struct {
	pool        *pgxpool.Pool
	fingerprint string
}

// NewCPHelpStore builds a store over the control-plane pool. fingerprint names
// the embedder that produced the vectors being written (see
// EmbedFingerprint) and is stored beside each chunk, so a later model change
// is detectable — see StaleFingerprints.
func NewCPHelpStore(pool *pgxpool.Pool, fingerprint string) *CPHelpStore {
	return &CPHelpStore{pool: pool, fingerprint: fingerprint}
}

// Compile-time proof CPHelpStore is a sink ingest.IngestFS can write to and
// ingest.PruneMissing can prune.
var (
	_ ingest.HelpStore = (*CPHelpStore)(nil)
	_ ingest.DocPruner = (*CPHelpStore)(nil)
)

// EmbedFingerprint is the storable form of an embedder's rag.Fingerprinter
// value ("" when it has none). The raw fingerprint separates model and prefix
// with a NUL byte, which Postgres TEXT cannot hold.
func EmbedFingerprint(emb rag.Embedder) string {
	f, ok := emb.(rag.Fingerprinter)
	if !ok {
		return ""
	}
	return strings.ReplaceAll(f.Fingerprint(), "\x00", "|")
}

// PruneDocs deletes every doc whose key is not in keep — a doc removed from
// the corpus otherwise keeps answering questions forever.
func (s *CPHelpStore) PruneDocs(ctx context.Context, keep []string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM cp_rag_chunks WHERE NOT (doc_key = ANY($1))`, keep)
	if err != nil {
		return 0, fmt.Errorf("prune help docs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// StaleFingerprints returns the distinct embed fingerprints stored in
// cp_rag_chunks that differ from current. Non-empty means some help vectors
// were produced by a different embedder and are no longer comparable with
// query vectors — the help corpus needs a reindex.
func (s *CPHelpStore) StaleFingerprints(ctx context.Context, current string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT COALESCE(embed_fingerprint, '') FROM cp_rag_chunks WHERE COALESCE(embed_fingerprint, '') <> $1`, current)
	if err != nil {
		return nil, fmt.Errorf("help fingerprints: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, fmt.Errorf("help fingerprints: %w", err)
		}
		out = append(out, fp)
	}
	return out, rows.Err()
}

// ReplaceDoc atomically replaces every chunk for docKey — delete then insert in
// one transaction. This is what makes re-ingestion idempotent: a document whose
// sections were renamed or removed does not leave orphaned chunks behind to be
// retrieved forever, which a plain upsert keyed on section would.
func (s *CPHelpStore) ReplaceDoc(ctx context.Context, docKey string, chunks []ingest.DocChunk) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM cp_rag_chunks WHERE doc_key = $1`, docKey); err != nil {
		return fmt.Errorf("delete existing chunks for %s: %w", docKey, err)
	}
	for _, c := range chunks {
		_, err := tx.Exec(ctx,
			`INSERT INTO cp_rag_chunks (doc_key, section, content, embedding, embed_fingerprint) VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
			docKey, c.Section, c.Content, pgvector.NewVector(c.Embedding), s.fingerprint)
		if err != nil {
			return fmt.Errorf("insert chunk %q for %s: %w", c.Section, docKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
