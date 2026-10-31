package tenancy

import (
	"net/http"
	"strconv"
	"sync"
)

const (
	// migratingMessage is the 503 body shown while a tenant's boot migration runs.
	migratingMessage = "Your workspace is being updated. Please retry shortly."
	// migratingRetryAfterSeconds is the Retry-After hint sent with that 503.
	migratingRetryAfterSeconds = 5
)

// MigrationGate is a thread-safe set of tenant IDs still migrating at boot.
// A nil gate is valid and gates nothing.
type MigrationGate struct {
	mu  sync.RWMutex
	ids map[string]struct{}
}

// NewMigrationGate returns an empty gate.
func NewMigrationGate() *MigrationGate {
	return &MigrationGate{ids: make(map[string]struct{})}
}

// Begin marks the tenant as migrating.
func (g *MigrationGate) Begin(tenantID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.ids[tenantID] = struct{}{}
	g.mu.Unlock()
}

// Done clears the tenant's migrating mark.
func (g *MigrationGate) Done(tenantID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.ids, tenantID)
	g.mu.Unlock()
}

// Migrating reports whether the tenant is still being migrated.
func (g *MigrationGate) Migrating(tenantID string) bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	_, ok := g.ids[tenantID]
	g.mu.RUnlock()
	return ok
}

// rejectIfMigrating writes a 503 with Retry-After and returns true when the
// gate reports the tenant as migrating.
func rejectIfMigrating(w http.ResponseWriter, g *MigrationGate, tenantID string) bool {
	if !g.Migrating(tenantID) {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(migratingRetryAfterSeconds))
	writeErr(w, http.StatusServiceUnavailable, migratingMessage)
	return true
}
