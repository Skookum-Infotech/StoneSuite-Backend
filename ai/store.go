package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// buildScopedSearch returns the parameterized SQL + args for a scope-safe
// similarity search over rag_chunks. $1 is ALWAYS the query vector; scope
// params follow. The scope clause is ANDed onto the ORDER BY — it can only
// narrow the caller's permitted rows (mirrors workflow.buildRecordQuery).
// Never interpolate a value; only this fixed set of clause shapes is used.
func buildScopedSearch(scope, callerUserID string, teamIDs []string, k int) (string, []any) {
	args := []any{nil} // filled with the vector by the caller ($1)
	where := "TRUE"
	switch scope {
	case "team":
		where = "(owner_user_id = $2 OR team_id = ANY($3))"
		args = append(args, callerUserID, teamIDs)
	case "own":
		where = "owner_user_id = $2"
		args = append(args, callerUserID)
	case "all":
		// no narrowing
	default:
		where = "FALSE" // unknown scope denies everything (fail closed)
	}
	sql := fmt.Sprintf(
		`SELECT source_id, content FROM rag_chunks WHERE %s ORDER BY embedding <=> $1 LIMIT %d`,
		where, k)
	return sql, args
}

// RagStore is the tenant-side vector store. It implements ai/index.ChunkSink
// (Upsert/Delete, the ingestion write path) and ai.Retriever's tenant half
// (SearchScoped, the RBAC-scoped read path).
type RagStore struct{ pool *pgxpool.Pool }

// NewRagStore builds a store over a tenant pool.
func NewRagStore(pool *pgxpool.Pool) *RagStore { return &RagStore{pool: pool} }

// Upsert writes (or refreshes) one chunk's content + embedding, keyed by
// source_id (unique-indexed — see schema.sql). Called by the index worker
// after rendering + embedding a record.
func (s *RagStore) Upsert(ctx context.Context, c Chunk) error {
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

// Delete removes a chunk by source_id. A no-op (not an error) if it doesn't exist.
func (s *RagStore) Delete(ctx context.Context, sourceID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM rag_chunks WHERE source_id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("rag chunk delete: %w", err)
	}
	return nil
}

// SearchScoped returns up to k chunks most similar to queryVec that the
// caller (granted `scope`) is permitted to read.
func (s *RagStore) SearchScoped(ctx context.Context, queryVec []float32, scope, callerUserID string, teamIDs []string, k int) ([]Citation, error) {
	sql, args := buildScopedSearch(scope, callerUserID, teamIDs, k)
	args[0] = pgvector.NewVector(queryVec)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("scoped search: %w", err)
	}
	defer rows.Close()
	var out []Citation
	for rows.Next() {
		var sourceID, content string
		if err := rows.Scan(&sourceID, &content); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, Citation{SourceType: "record", SourceID: sourceID, Snippet: snippet(content), Content: groundingContent(content)})
	}
	return out, rows.Err()
}

// snippet trims a chunk's content to a single-line preview for citations.
// Display only — never used to ground an LLM answer, see groundingContent.
func snippet(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// groundingLimit caps how much of a stored chunk is passed to the LLM as
// context. Chunks already have to fit the embedder's ~512-token context
// window to have been embedded at all (see ai/helpdocs.IngestFS), so this is
// a generous safety net rather than the primary size control — unlike
// snippet, which is a deliberately short one-line UI preview.
const groundingLimit = 2000

// groundingContent returns the chunk content the LLM is grounded in,
// preserving structure (newlines, markdown tables) that snippet's
// single-line preview intentionally discards.
func groundingContent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > groundingLimit {
		return s[:groundingLimit] + "…"
	}
	return s
}
