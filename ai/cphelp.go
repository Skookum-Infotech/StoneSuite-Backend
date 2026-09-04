package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Skookum-Infotech/go-rag/ingest"
)

// CPHelpStore is the INGESTION path for app-help chunks in the control-plane
// pool's cp_rag_chunks table. Retrieval goes through HelpCorpus instead.
//
// The content is identical for every tenant and is nobody's private data,
// which is why it lives in the control plane rather than being duplicated per
// tenant, and why its corpus carries no scope filter.
type CPHelpStore struct{ pool *pgxpool.Pool }

// NewCPHelpStore builds a store over the control-plane pool.
func NewCPHelpStore(pool *pgxpool.Pool) *CPHelpStore { return &CPHelpStore{pool: pool} }

// Compile-time proof CPHelpStore is a sink ingest.IngestFS can write to.
var _ ingest.HelpStore = (*CPHelpStore)(nil)

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
			`INSERT INTO cp_rag_chunks (doc_key, section, content, embedding) VALUES ($1, $2, $3, $4)`,
			docKey, c.Section, c.Content, pgvector.NewVector(c.Embedding))
		if err != nil {
			return fmt.Errorf("insert chunk %q for %s: %w", c.Section, docKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
