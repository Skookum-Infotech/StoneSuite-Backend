package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// RagStore is the tenant-side vector store. It implements ai/index.ChunkSink
// (Upsert/Delete, the ingestion write path). Scoped retrieval (the read path,
// SearchScoped) is added to this same type in Plan 3.
type RagStore struct{ pool *pgxpool.Pool }

// NewRagStore builds a store over a tenant pool.
func NewRagStore(pool *pgxpool.Pool) *RagStore { return &RagStore{pool: pool} }

// Upsert writes (or refreshes) one chunk's content + embedding, keyed by
// source_id (unique-indexed — see schema.sql). Called by the index worker
// after rendering + embedding a record.
func (s *RagStore) Upsert(ctx context.Context, c Chunk) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rag_chunks (source_id, workflow_id, owner_user_id, team_id, content, content_hash, embedding, updated_at)
		VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, $7, NOW())
		ON CONFLICT (source_id) DO UPDATE SET
			workflow_id   = EXCLUDED.workflow_id,
			owner_user_id = EXCLUDED.owner_user_id,
			team_id       = EXCLUDED.team_id,
			content       = EXCLUDED.content,
			content_hash  = EXCLUDED.content_hash,
			embedding     = EXCLUDED.embedding,
			updated_at    = NOW()`,
		c.SourceID, c.WorkflowID, c.OwnerUserID, c.TeamID, c.Content, c.ContentHash, pgvector.NewVector(c.Embedding))
	if err != nil {
		return fmt.Errorf("rag chunk upsert: %w", err)
	}
	return nil
}

// Delete removes a chunk by source_id. A no-op (not an error) if it doesn't exist.
func (s *RagStore) Delete(ctx context.Context, sourceID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM rag_chunks WHERE source_id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("rag chunk delete: %w", err)
	}
	return nil
}
