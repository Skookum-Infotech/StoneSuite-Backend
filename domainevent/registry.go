package domainevent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
)

// Handler processes one event inside a savepoint of the drainer's transaction.
type Handler func(ctx context.Context, tx pgx.Tx, ev Stored) error

// Registry maps consumer names to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}}
}

// Register adds a consumer; an empty name, nil handler or duplicate is an error.
func (r *Registry) Register(name string, h Handler) error {
	if name == "" {
		return errors.New("domainevent: consumer name is required")
	}
	if h == nil {
		return fmt.Errorf("domainevent: consumer %q has a nil handler", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[name]; dup {
		return fmt.Errorf("domainevent: consumer %q already registered", name)
	}
	r.handlers[name] = h
	return nil
}

// Names returns the registered consumer names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Handler returns the handler registered under name.
func (r *Registry) Handler(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[name]
	return h, ok
}
