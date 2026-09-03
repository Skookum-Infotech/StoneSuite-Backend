package globalsearch

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// PerGroupCap is the maximum number of results returned per module group.
const PerGroupCap = 6

// fanoutTimeout bounds the whole fan-out so one slow module can't hang the
// request indefinitely; each provider's own query still respects it via ctx
// cancellation.
const fanoutTimeout = 5 * time.Second

// Group is one module's slice of results in the response.
type Group struct {
	Results []Result `json:"results"`
	HasMore bool     `json:"hasMore"`
}

// Response is the full grouped global-search payload.
type Response struct {
	Query  string           `json:"query"`
	Groups map[string]Group `json:"groups"`
}

// Search fans term out to every registered provider the caller has read
// access to, in parallel, and merges the results into one grouped response.
// A provider the caller lacks permission for, or whose query errors, is
// simply omitted — global search stays resilient to one module's issue and
// never fails the whole request over a single denied or broken group.
// modules, if non-empty, restricts the fan-out to that allowlist of registry
// keys (unknown keys are ignored — it's a convenience filter, not a security
// boundary; RBAC is still enforced per-provider regardless of this list).
func Search(ctx context.Context, pool *pgxpool.Pool, identityID, term string, modules []string) Response {
	term = strings.TrimSpace(term)
	providers := selectProviders(modules)

	ctx, cancel := context.WithTimeout(ctx, fanoutTimeout)
	defer cancel()

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = Response{Query: term, Groups: map[string]Group{}}
	)
	for _, p := range providers {
		wg.Add(1)
		go func(p Provider) {
			defer wg.Done()

			decision, err := authz.Check(ctx, pool, identityID, p.Resource, authz.ActionRead)
			if err != nil || !decision.Allowed {
				return // no permission => omit the group (not a security event; see globalsearch/search.go doc)
			}
			results, hasMore, err := p.Search(ctx, pool, decision.Scope, identityID, term, PerGroupCap)
			if err != nil {
				slog.Warn("global search provider failed", "module", p.Key, "error", err)
				return
			}
			mu.Lock()
			out.Groups[p.Key] = Group{Results: results, HasMore: hasMore}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out
}

// selectProviders returns every registered provider, or only those whose key
// is in modules when modules is non-empty.
func selectProviders(modules []string) []Provider {
	all := All()
	if len(modules) == 0 {
		return all
	}
	allow := make(map[string]bool, len(modules))
	for _, m := range modules {
		allow[strings.TrimSpace(m)] = true
	}
	out := make([]Provider, 0, len(all))
	for _, p := range all {
		if allow[p.Key] {
			out = append(out, p)
		}
	}
	return out
}
