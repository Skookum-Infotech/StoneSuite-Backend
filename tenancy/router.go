package tenancy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

// DSNResolver turns a tenant's stored connection reference into a usable
// Postgres DSN. This is the single seam where secret decryption happens; the
// default resolver treats DBConnectionRef as a plaintext DSN (dev/local), and
// Phase 1 swaps in a secret-manager-backed resolver without touching callers.
type DSNResolver func(ctx context.Context, t *Tenant) (string, error)

// PlainDSNResolver is the default resolver: the stored reference IS the DSN.
func PlainDSNResolver(_ context.Context, t *Tenant) (string, error) {
	if t.DBConnectionRef == "" {
		return "", fmt.Errorf("tenant %s has no db_connection_ref", t.Slug)
	}
	return t.DBConnectionRef, nil
}

// Router hands out per-tenant connection pools, opening each lazily on first
// use and caching it. At ~30 tenants we keep every pool warm (no eviction);
// each pool is capped low so total connections stay bounded.
type Router struct {
	resolve  DSNResolver
	maxConns int32
	mu       sync.RWMutex
	pools    map[string]*pgxpool.Pool // keyed by tenant ID
}

// NewRouter builds a router. Pass nil resolver to use PlainDSNResolver.
func NewRouter(resolve DSNResolver) *Router {
	if resolve == nil {
		resolve = PlainDSNResolver
	}
	return &Router{
		resolve:  resolve,
		maxConns: 5,
		pools:    make(map[string]*pgxpool.Pool),
	}
}

// PoolFor returns the connection pool for a tenant, opening it if needed.
func (r *Router) PoolFor(ctx context.Context, t *Tenant) (*pgxpool.Pool, error) {
	// Fast path: already cached.
	r.mu.RLock()
	if p, ok := r.pools[t.ID]; ok {
		r.mu.RUnlock()
		return p, nil
	}
	r.mu.RUnlock()

	// Slow path: open and cache under write lock (double-check to avoid races).
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[t.ID]; ok {
		return p, nil
	}

	dsn, err := r.resolve(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("resolve dsn for tenant %s: %w", t.Slug, err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn for tenant %s: %w", t.Slug, err)
	}

	cfg.MaxConns = r.maxConns
	cfg.MaxConnLifetime = time.Hour
	// Register the pgvector `vector` type on each new connection, for RAG
	// queries. Tolerant: a tenant whose migrations haven't run yet (extension
	// not installed) must not lose its whole pool over this — only its RAG
	// queries fail, and self-heals once the migration + a fresh connection land.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
			slog.Warn("pgvector type registration failed", "tenant", t.Slug, "err", err)
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool for tenant %s: %w", t.Slug, err)
	}
	r.pools[t.ID] = pool
	return pool, nil
}

// Evict closes and removes a tenant's pool (e.g. after suspend/delete or a
// connection-string change). Safe to call for an unknown tenant.
func (r *Router) Evict(tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[tenantID]; ok {
		p.Close()
		delete(r.pools, tenantID)
	}
}

// Close releases every cached pool. Call on server shutdown.
func (r *Router) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.pools {
		p.Close()
		delete(r.pools, id)
	}
}
