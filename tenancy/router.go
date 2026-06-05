package tenancy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	resolve     DSNResolver
	maxConns    int32
	mu          sync.RWMutex
	pools       map[string]*pgxpool.Pool // keyed by tenant ID
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
		fmt.Printf("[POOL DEBUG] Cache HIT for tenant %s (ID: %s)\n", t.Slug, t.ID)
		return p, nil
	}
	r.mu.RUnlock()

	// Slow path: open and cache under write lock (double-check to avoid races).
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[t.ID]; ok {
		fmt.Printf("[POOL DEBUG] Cache HIT after acquire lock for tenant %s\n", t.Slug)
		return p, nil
	}

	fmt.Printf("[POOL DEBUG] Opening pool for tenant %s (ID: %s, DBName: %s)\n", t.Slug, t.ID, t.DBName)
	fmt.Printf("[POOL DEBUG] DBConnectionRef (first 50 chars): %.50s...\n", t.DBConnectionRef)

	dsn, err := r.resolve(ctx, t)
	if err != nil {
		fmt.Printf("[POOL DEBUG] ERROR resolving DSN: %v\n", err)
		return nil, fmt.Errorf("resolve dsn for tenant %s: %w", t.Slug, err)
	}
	fmt.Printf("[POOL DEBUG] Resolved DSN (first 50 chars): %.50s...\n", dsn)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		fmt.Printf("[POOL DEBUG] ERROR parsing DSN: %v\n", err)
		return nil, fmt.Errorf("parse dsn for tenant %s: %w", t.Slug, err)
	}
	fmt.Printf("[POOL DEBUG] DSN parsed successfully\n")

	cfg.MaxConns = r.maxConns
	cfg.MaxConnLifetime = time.Hour

	fmt.Printf("[POOL DEBUG] Creating connection pool with max conns: %d\n", r.maxConns)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Printf("[POOL DEBUG] ERROR creating pool: %v (type: %T)\n", err, err)
		return nil, fmt.Errorf("open pool for tenant %s: %w", t.Slug, err)
	}
	fmt.Printf("[POOL DEBUG] Pool created successfully for tenant %s\n", t.Slug)
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
