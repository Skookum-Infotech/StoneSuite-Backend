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

// PerGroupCap is the default number of results returned per module group (the
// type-ahead dropdown). MaxPerGroupCap bounds what the full results page may ask
// for via the limit parameter.
const (
	PerGroupCap    = 6
	MaxPerGroupCap = 50
)

// clampCap keeps a caller-supplied per-group limit in [1, MaxPerGroupCap],
// defaulting to PerGroupCap when unset or invalid.
func clampCap(n int) int {
	switch {
	case n <= 0:
		return PerGroupCap
	case n > MaxPerGroupCap:
		return MaxPerGroupCap
	default:
		return n
	}
}

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
// perGroup caps how many results each group returns; it is clamped to
// [1, MaxPerGroupCap] and defaults to PerGroupCap when zero.
func Search(ctx context.Context, pool *pgxpool.Pool, identityID, term string, modules []string, perGroup int) Response {
	term = strings.TrimSpace(term)
	providers := selectProviders(modules)
	groupCap := clampCap(perGroup)

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
			results, hasMore, err := p.Search(ctx, pool, decision.Scope, identityID, term, groupCap)
			if err != nil {
				slog.Warn("global search provider failed", "module", p.Key, "error", err)
				return
			}
			// Stamp routing centrally so each SearchFunc stays unconcerned with
			// where the frontend renders its detail page (mirrors
			// controllers/dashboard_recent.go's fetchAllRecent).
			for i := range results {
				results[i].Domain = p.Domain
				results[i].Module = p.Module
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
