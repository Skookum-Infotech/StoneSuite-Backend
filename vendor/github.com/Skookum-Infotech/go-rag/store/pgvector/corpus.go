// Package pgvector implements rag.Corpus over a Postgres table holding chunk
// text, a pgvector embedding, and a generated tsvector — the two arms of hybrid
// retrieval in one table.
package pgvector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgv "github.com/pgvector/pgvector-go"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// Options configures a Corpus. Table and Name are required; the rest have
// working defaults.
type Options struct {
	// Table is the chunk table. It is interpolated into SQL, so it must be a
	// trusted identifier from your own configuration — never a value that
	// reached you from a request.
	Table string

	// Name identifies this corpus in citations and logs (rag.Corpus.Name).
	Name string

	// IDColumn is the column whose value becomes Citation.SourceID — whatever
	// the caller needs to navigate back to the source. It differs by corpus:
	// a record table keys on an opaque row id, a documentation table on a
	// human-readable section heading. Interpolated into SQL, so it must be a
	// trusted identifier. Defaults to DefaultIDColumn.
	IDColumn string

	// Scope narrows the corpus to what the caller may see. A nil Scope denies
	// everything: see rag.ScopeFilter, and prefer passing rag.AllowAll
	// explicitly for corpora that genuinely carry no restrictions.
	Scope rag.ScopeFilter

	// SnippetLimit caps the single-line preview shown in a citation.
	// Defaults to DefaultSnippetLimit.
	SnippetLimit int

	// GroundingLimit caps how much of a chunk is handed to the model as
	// context. Defaults to DefaultGroundingLimit.
	GroundingLimit int

	// EFSearch sets hnsw.ef_search for the vector query (transaction-local).
	// HNSW applies WHERE clauses AFTER the index scan, so a narrow scope (a
	// user who owns a few rows of a large table) can come back with fewer
	// than k rows — or none — at the default of 40. Raising it widens the
	// candidate set the filter runs over. 0 leaves the server default.
	EFSearch int
}

// Defaults tuned for a small self-hosted chat model on a CPU-bound box: a long
// prompt means long prefill before generation even starts. Raise
// DefaultGroundingLimit only alongside more chat-model compute.
const (
	DefaultSnippetLimit   = 240
	DefaultGroundingLimit = 1200
	DefaultIDColumn       = "source_id"
)

// Corpus is a rag.Corpus backed by one pgvector table. It is constructed for a
// particular caller — the scope filter is fixed here, not passed per search —
// so a Corpus value cannot be used to reach rows its owner may not see.
type Corpus struct {
	pool     *pgxpool.Pool
	table    string
	name     string
	idCol    string
	scope    rag.ScopeFilter
	snippet  int
	grounded int
	efSearch int
}

// Compile-time proof Corpus satisfies the interface the orchestrator wants.
var _ rag.Corpus = (*Corpus)(nil)

// New builds a Corpus over pool. A nil Options.Scope yields a corpus that
// matches nothing, which is the safe direction to fail.
func New(pool *pgxpool.Pool, opts Options) *Corpus {
	c := &Corpus{
		pool:     pool,
		table:    opts.Table,
		name:     opts.Name,
		idCol:    opts.IDColumn,
		scope:    opts.Scope,
		snippet:  opts.SnippetLimit,
		grounded: opts.GroundingLimit,
		efSearch: opts.EFSearch,
	}
	if c.snippet <= 0 {
		c.snippet = DefaultSnippetLimit
	}
	if c.grounded <= 0 {
		c.grounded = DefaultGroundingLimit
	}
	if c.idCol == "" {
		c.idCol = DefaultIDColumn
	}
	return c
}

// Name identifies the corpus in citations and logs.
func (c *Corpus) Name() string { return c.name }

// buildVectorSearch returns the parameterized nearest-neighbour query. $1 is
// always the query vector; the scope filter numbers its own parameters from $2.
//
// Only the table name and k — both trusted, k an int — are interpolated. Every
// caller-derived value travels as a placeholder argument. The scope clause is
// ANDed, so it can only ever narrow the result set.
func (c *Corpus) buildVectorSearch(k int) (string, []any) {
	where, scopeArgs := c.scope.Clause(2)
	sql := fmt.Sprintf(
		`SELECT %s, content, embedding <=> $1 AS distance FROM %s WHERE (%s) ORDER BY distance LIMIT %d`,
		c.idCol, c.table, where, k)
	return sql, append([]any{nil}, scopeArgs...)
}

// buildLexicalSearch returns the parameterized full-text query. $1 is always
// the cleaned query text (see LexicalQuery); the scope filter numbers its own
// parameters from $2.
//
// 'simple' rather than 'english': no stemming, so identifiers like
// INC-2023-Q4-011 survive tokenisation intact — precisely the rare tokens a
// 768-dim embedding blurs and this arm exists to catch.
func (c *Corpus) buildLexicalSearch(k int) (string, []any) {
	where, scopeArgs := c.scope.Clause(2)
	sql := fmt.Sprintf(
		`SELECT %s, content FROM %s WHERE (%s) AND content_tsv @@ websearch_to_tsquery('simple', $1) `+
			`ORDER BY ts_rank_cd(content_tsv, websearch_to_tsquery('simple', $1)) DESC LIMIT %d`,
		c.idCol, c.table, where, k)
	return sql, append([]any{nil}, scopeArgs...)
}

// SearchVector returns up to k nearest neighbours of vec that the caller may
// see, with Distance populated for the orchestrator's relevance floor.
func (c *Corpus) SearchVector(ctx context.Context, vec []float32, k int) ([]rag.Citation, error) {
	sql, args := c.buildVectorSearch(k)
	args[0] = pgv.NewVector(vec)

	var q querier = c.pool
	if c.efSearch > 0 {
		tx, err := c.pool.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s vector search: begin: %w", c.name, err)
		}
		// Read-only, so rollback is the correct end state either way.
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`, strconv.Itoa(c.efSearch)); err != nil {
			return nil, fmt.Errorf("%s vector search: set ef_search: %w", c.name, err)
		}
		q = tx
	}

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s vector search: %w", c.name, err)
	}
	defer rows.Close()

	var out []rag.Citation
	for rows.Next() {
		var sourceID, content string
		var distance float64
		if err := rows.Scan(&sourceID, &content, &distance); err != nil {
			return nil, fmt.Errorf("%s scan: %w", c.name, err)
		}
		out = append(out, c.citation(sourceID, content, distance, true))
	}
	return out, rows.Err()
}

// querier is the one method SearchVector needs from either the pool or a tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SearchLexical returns up to k full-text matches the caller may see. Distance
// is left invalid and Lexical set: a literal term match needs no similarity
// floor. A question with no content words left after LexicalQuery searches
// nothing rather than matching arbitrary rows.
func (c *Corpus) SearchLexical(ctx context.Context, query string, k int) ([]rag.Citation, error) {
	cleaned := LexicalQuery(query)
	if cleaned == "" {
		return nil, nil
	}
	sql, args := c.buildLexicalSearch(k)
	args[0] = cleaned

	rows, err := c.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s lexical search: %w", c.name, err)
	}
	defer rows.Close()

	var out []rag.Citation
	for rows.Next() {
		var sourceID, content string
		if err := rows.Scan(&sourceID, &content); err != nil {
			return nil, fmt.Errorf("%s scan: %w", c.name, err)
		}
		cite := c.citation(sourceID, content, 0, false)
		cite.Lexical = true
		out = append(out, cite)
	}
	return out, rows.Err()
}

// citation builds the UI preview and the model-facing grounding text from one
// stored chunk. They are deliberately separate: truncating the preview to one
// short line must never also truncate what the model reasons over.
func (c *Corpus) citation(sourceID, content string, distance float64, distanceValid bool) rag.Citation {
	return rag.Citation{
		SourceType:    c.name,
		SourceID:      sourceID,
		Snippet:       truncateLine(content, c.snippet),
		Content:       truncate(strings.TrimSpace(content), c.grounded),
		Distance:      distance,
		DistanceValid: distanceValid,
	}
}

// truncateLine flattens to a single line and caps length — display only.
func truncateLine(s string, limit int) string {
	return truncate(strings.ReplaceAll(s, "\n", " "), limit)
}

// truncate caps s at limit bytes, preserving structure (newlines, markdown
// tables) that truncateLine intentionally discards. The cut backs up to a rune
// boundary so a multi-byte character is never split into invalid UTF-8.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
