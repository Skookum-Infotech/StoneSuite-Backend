package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// HelpChunk is one embeddable section of an app-help document, ready to
// upsert into cp_rag_chunks.
type HelpChunk struct {
	Section   string
	Content   string
	Embedding []float32
}

// CPHelpStore retrieves app-help/documentation chunks from the control-plane
// pool's cp_rag_chunks table. Deliberately has NO scope clause: this content
// is identical for every tenant and not anyone's private data.
type CPHelpStore struct{ pool *pgxpool.Pool }

// NewCPHelpStore builds a store over the control-plane pool.
func NewCPHelpStore(pool *pgxpool.Pool) *CPHelpStore { return &CPHelpStore{pool: pool} }

// Search returns up to k app-help chunks most similar to queryVec.
func (s *CPHelpStore) Search(ctx context.Context, queryVec []float32, k int) ([]Citation, error) {
	sql := fmt.Sprintf(`SELECT section, content FROM cp_rag_chunks ORDER BY embedding <=> $1 LIMIT %d`, k)
	rows, err := s.pool.Query(ctx, sql, pgvector.NewVector(queryVec))
	if err != nil {
		return nil, fmt.Errorf("help search: %w", err)
	}
	defer rows.Close()
	var out []Citation
	for rows.Next() {
		var section, content string
		if err := rows.Scan(&section, &content); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, Citation{SourceType: "help", SourceID: section, Snippet: snippet(content)})
	}
	return out, rows.Err()
}

// ReplaceDoc atomically replaces every chunk for docKey with chunks (delete
// then insert in one transaction) — the idempotent ingestion pattern the
// rag-ingest-help CLI uses so re-running it never accumulates stale sections.
func (s *CPHelpStore) ReplaceDoc(ctx context.Context, docKey string, chunks []HelpChunk) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

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
