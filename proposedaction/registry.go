package proposedaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
)

// Executor applies one kind of proposal for one resource. It is defined here,
// on the consumer side; module packages provide implementations.
type Executor interface {
	// Resource is the RBAC resource whose permission gates confirmation.
	Resource() authz.Resource
	// Validate checks the payload; the store runs it at propose and again at confirm.
	Validate(ctx context.Context, payload json.RawMessage) error
	// Preview renders the human-readable summary stored with the proposal.
	Preview(ctx context.Context, payload json.RawMessage) (Preview, error)
	// InScope is the row-level ownership check for updates under scope; creates return true.
	InScope(ctx context.Context, q Querier, scope authz.Scope, caller Caller, kind Kind, payload json.RawMessage) (bool, error)
	// Execute performs the write inside tx. It MUST call the module's existing
	// store code (never raw SQL) so validation, numbering, audit and approval
	// behave exactly as for a human request.
	Execute(ctx context.Context, tx pgx.Tx, caller Caller, payload json.RawMessage) (ResultRef, error)
}

type registryKey struct {
	resource string
	kind     Kind
}

// Registry maps (resource, kind) to an Executor. Build it once at startup.
type Registry struct {
	m map[registryKey]Executor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[registryKey]Executor{}} }

// Register adds an executor for its resource and kind; duplicates, nil
// executors and unknown kinds are errors.
func (r *Registry) Register(kind Kind, e Executor) error {
	if e == nil {
		return errors.New("register proposed action executor: nil executor")
	}
	if !validKind(kind) {
		return fmt.Errorf("register proposed action executor: unknown kind %q", kind)
	}
	res := string(e.Resource())
	if res == "" {
		return errors.New("register proposed action executor: empty resource")
	}
	k := registryKey{resource: res, kind: kind}
	if _, dup := r.m[k]; dup {
		return fmt.Errorf("register proposed action executor: duplicate %s/%s", res, kind)
	}
	r.m[k] = e
	return nil
}

// Lookup returns the executor registered for resource and kind.
func (r *Registry) Lookup(resource string, kind Kind) (Executor, bool) {
	if r == nil {
		return nil, false
	}
	e, ok := r.m[registryKey{resource: resource, kind: kind}]
	return e, ok
}
