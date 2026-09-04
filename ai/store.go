package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai/index"
)

// RagStore is the tenant-side INGESTION path for rag_chunks: it writes what the
// index worker produces and reads back only the bookkeeping needed to decide
// whether a re-embed is required.
//
// Retrieval does not live here. Reads go through the scoped corpus the adapter
// builds (see RecordsCorpus), which is what keeps the RBAC scope clause in one
// place rather than duplicated across a read and a write type.
type RagStore struct{ pool *pgxpool.Pool }

// NewRagStore builds a store over a tenant pool.
func NewRagStore(pool *pgxpool.Pool) *RagStore { return &RagStore{pool: pool} }

// Compile-time proof RagStore is a sink the index worker can write through.
// Asserted here rather than in ai/index so the worker package stays free of
// any StoneSuite import and could be lifted into go-rag unchanged.
var _ index.ChunkSink = (*RagStore)(nil)

// Upsert writes (or refreshes) one chunk's content, scope columns, and
// embedding, keyed by source_id.
func (s *RagStore) Upsert(ctx context.Context, c rag.Chunk) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rag_chunks (source_id, workflow_id, owner_user_id, team_id, content, content_hash, embedding, updated_at)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, $7, NOW())
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

// Meta reads a chunk's hash and scope columns by source_id. found is false when
// the record has never been indexed. NULL uuid columns come back as "" so they
// compare directly against rag.Chunk's string fields, matching how Upsert
// writes them (NULLIF($n,”)::uuid).
func (s *RagStore) Meta(ctx context.Context, sourceID string) (m rag.ChunkMeta, found bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT content_hash,
		       COALESCE(workflow_id::text, ''),
		       COALESCE(owner_user_id::text, ''),
		       COALESCE(team_id::text, '')
		FROM rag_chunks WHERE source_id = $1`, sourceID,
	).Scan(&m.ContentHash, &m.WorkflowID, &m.OwnerUserID, &m.TeamID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return rag.ChunkMeta{}, false, nil
		}
		return rag.ChunkMeta{}, false, fmt.Errorf("rag chunk meta: %w", err)
	}
	return m, true, nil
}

// UpdateScope refreshes only the scope columns, leaving content and embedding
// untouched. This is the cheap path for a record whose text is unchanged but
// whose ownership moved — re-embedding identical text to correct an owner id
// would be pure waste, but leaving the stale owner in place is a scope bug.
func (s *RagStore) UpdateScope(ctx context.Context, c rag.Chunk) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE rag_chunks SET
			workflow_id   = NULLIF($2, '')::uuid,
			owner_user_id = NULLIF($3, '')::uuid,
			team_id       = NULLIF($4, '')::uuid,
			updated_at    = NOW()
		WHERE source_id = $1`,
		c.SourceID, c.WorkflowID, c.OwnerUserID, c.TeamID)
	if err != nil {
		return fmt.Errorf("rag chunk scope update: %w", err)
	}
	return nil
}

// Delete removes a chunk by source_id. A no-op (not an error) if absent.
func (s *RagStore) Delete(ctx context.Context, sourceID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM rag_chunks WHERE source_id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("rag chunk delete: %w", err)
	}
	return nil
}
