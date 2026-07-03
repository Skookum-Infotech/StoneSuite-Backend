package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

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
