package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/route"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// routeFieldWhitelist is the fixed vocabulary the LLM router may name in a
// Filter.Field — deliberately smaller than a workflow's full FieldResolver
// surface. owner_user_id/team_id/id are scope's job, not the model's (see
// route's package doc); custom fields are left out because they vary per
// tenant and per workflow, and folding a tenant's whole custom-field schema
// into the routing prompt is a bigger feature than this pass justifies. These
// four system fields cover the filters Phase 3 exists to unblock ("closed
// last quarter", "qualified leads").
var routeFieldWhitelist = []string{"status", "created_at", "updated_at", "record_number"}

var routeFieldWhitelistSet = func() map[string]bool {
	m := make(map[string]bool, len(routeFieldWhitelist))
	for _, f := range routeFieldWhitelist {
		m[f] = true
	}
	return m
}()

// routeOperatorSet bounds Filter.Op to comparisons that take a single scalar
// value (route.Filter.Value is always a string) — OpIn/OpBetween need
// multi-value input the router's schema doesn't offer, so they're rejected
// here rather than mis-coerced.
var routeOperatorSet = map[query.Operator]bool{
	query.OpEq: true, query.OpNeq: true, query.OpGt: true, query.OpGte: true,
	query.OpLt: true, query.OpLte: true, query.OpContains: true, query.OpStartsWith: true,
	query.OpIsNull: true, query.OpIsEmpty: true,
}

// resolveRoutedFilteredCount attempts the LLM-routed filtered-count path: ask
// llm to classify question via route.Extract, and — only when the model
// returns intent=count with every filter inside routeFieldWhitelist/
// routeOperatorSet and every workflow key a real CRM key — execute a
// scope-composed, filtered count. Returns ok=false for every other outcome
// (routing unsupported, malformed model output, a non-count intent, a
// Route.NoFilter extraction failure, or a filter/key outside the whitelist),
// which the caller treats as "fall back to plain retrieval" — never a fatal
// error, per the architecture plan's rule that adversarial/malformed model
// output must degrade, not break, the ask.
//
// Security: grants and actorIdentityID are the caller's, resolved from request
// context before this function is ever reached (see AIOps.Ask) — nothing
// route.Extract returns can influence WHOSE data is counted, only WHAT is
// counted. That is what routeFieldWhitelist/routeOperatorSet exist to keep
// true even if a record's content tries to steer the model via indirect
// prompt injection: the model can at most choose a bad-but-still-whitelisted
// filter, never a scope or identity value.
//
// history is the caller's prior conversation turns, if any (see
// ai.ConversationStore.History) — passed straight to route.Extract so a
// follow-up like "what about last month?" can resolve against the earlier
// turn's context. Same untrusted-input caveat as any other conversation
// content reaching the model: it can steer word choice, never scope.
func resolveRoutedFilteredCount(ctx context.Context, llm ragcore.LLMClient, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID, question string, history []ragcore.Message) (ragcore.AskResult, bool) {
	if len(grants.Types()) == 0 {
		// Nothing countable: don't spend a model call deciding what to count.
		return ragcore.AskResult{}, false
	}
	keys := crmstore.CRMWorkflowKeys()

	// statusIDByName resolves a model-returned status NAME (what
	// WithAllowedValues constrains it to) to the id/code the store's filter
	// actually compares against (see statusAllowedValues) — best-effort: a
	// lookup failure just means no status allowed-values are offered, not a
	// fallback, since most routed counts don't filter on status at all.
	statuses, statusErr := store.AllStatuses(ctx, pool)
	if statusErr != nil {
		slog.Warn("ai query routing: status lookup failed; proceeding without status allowed-values", "err", statusErr)
	}
	statusNames, statusIDByName := statusAllowedValues(statuses)

	opts := []route.Option{route.WithToday(time.Now())}
	if len(statusNames) > 0 {
		opts = append(opts, route.WithAllowedValues(map[string][]string{"status": statusNames}))
	}
	r, err := route.Extract(ctx, llm, keys, routeFieldWhitelist, question, history, opts...)
	if err != nil {
		slog.Warn("ai query routing unavailable; falling back to retrieval", "err", err)
		return ragcore.AskResult{}, false
	}
	if r.Intent != route.IntentCount {
		return ragcore.AskResult{}, false
	}
	if r.NoFilter {
		// The model extracted neither a workflow key nor a filter for a
		// question we only routed BECAUSE it carried a filter-hint word
		// (hasFilterHintCountIntent) — that's a failed extraction, not "count
		// everything". Returning the unfiltered total here would silently
		// mislabel it as the answer to a filtered question.
		slog.Warn("ai query router returned no filter for a filter-hint question; falling back to retrieval")
		return ragcore.AskResult{}, false
	}

	matchedKeys := r.WorkflowKeys
	if len(matchedKeys) == 0 {
		matchedKeys = keys
	}
	validKeys := make(map[string]bool, len(keys))
	for _, k := range keys {
		validKeys[k] = true
	}
	for _, k := range matchedKeys {
		if !validKeys[k] {
			slog.Warn("ai query router named an unknown workflow key; falling back to retrieval", "key", k)
			return ragcore.AskResult{}, false
		}
	}

	clauses := make([]query.Clause, 0, len(r.Filters))
	for _, f := range r.Filters {
		if !routeFieldWhitelistSet[f.Field] {
			slog.Warn("ai query router named a field outside the routing whitelist; falling back to retrieval", "field", f.Field)
			return ragcore.AskResult{}, false
		}
		op := query.Operator(f.Op)
		if !routeOperatorSet[op] {
			slog.Warn("ai query router named an operator outside the routing whitelist; falling back to retrieval", "op", f.Op)
			return ragcore.AskResult{}, false
		}
		value := any(f.Value)
		if f.Field == "status" {
			id, ok := statusIDByName[strings.ToLower(f.Value)]
			if !ok {
				slog.Warn("ai query router named a status value outside the tenant's known statuses; falling back to retrieval", "value", f.Value)
				return ragcore.AskResult{}, false
			}
			value = id
		}
		clauses = append(clauses, query.Clause{Field: f.Field, Op: op, Value: value})
	}
	filterDesc := describeFilters(r.Filters)

	res, err := countCRMRecordsFiltered(ctx, store, pool, grants, actorIdentityID, matchedKeys, clauses, filterDesc)
	if err != nil {
		slog.Warn("ai routed count failed; falling back to retrieval", "err", err)
		return ragcore.AskResult{}, false
	}
	return res, true
}

// statusAllowedValues turns store.AllStatuses' result into the two things
// resolveRoutedFilteredCount needs: the deduplicated list of status names to
// offer route.WithAllowedValues (what the model may say), and a
// lowercased-name -> id/code lookup for turning the model's chosen name back
// into what the store's "status" filter actually compares against (see
// relationalSystemFields — the column holds an id, not the display name).
func statusAllowedValues(statuses []workflow.StatusInfo) (names []string, idByName map[string]string) {
	idByName = make(map[string]string, len(statuses))
	seen := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		key := strings.ToLower(s.StatusLabel)
		if _, ok := idByName[key]; !ok {
			idByName[key] = s.StateID
		}
		if !seen[s.StatusLabel] {
			seen[s.StatusLabel] = true
			names = append(names, s.StatusLabel)
		}
	}
	sort.Strings(names)
	return names, idByName
}

// describeFilters renders route.Extract's filters as the human-readable
// clause formatCountAnswer appends to a routed count's answer, e.g. "status
// New created since 2026-09-01" — using the model's ORIGINAL values (status
// names, raw date strings), never the id resolveRoutedFilteredCount
// substitutes for execution, so the answer reads the way the caller asked it.
func describeFilters(filters []route.Filter) string {
	parts := make([]string, 0, len(filters))
	for _, f := range filters {
		parts = append(parts, describeFilter(f))
	}
	return strings.Join(parts, ", ")
}

// describeFilter renders one filter clause; see describeFilters.
func describeFilter(f route.Filter) string {
	switch f.Field {
	case "status":
		return "status " + f.Value
	case "created_at":
		return describeDateFilter("created", f.Op, f.Value)
	case "updated_at":
		return describeDateFilter("updated", f.Op, f.Value)
	case "record_number":
		return "record number " + f.Value
	default:
		return f.Field + " " + f.Value
	}
}

// describeDateFilter renders a created_at/updated_at clause as "<verb> since
// <date>" (gt/gte), "<verb> before <date>" (lt/lte), or "<verb> on <date>"
// (eq/anything else).
func describeDateFilter(verb, op, val string) string {
	switch query.Operator(op) {
	case query.OpGt, query.OpGte:
		return verb + " since " + val
	case query.OpLt, query.OpLte:
		return verb + " before " + val
	default:
		return verb + " on " + val
	}
}

// countCRMRecordsFiltered is CountRecordsFiltered summed across keys — the
// filtered counterpart to countCRMRecords, for the LLM-routed count path.
// filterDesc (see describeFilters) is stated in the answer so the caller
// knows which filters were actually applied.
func countCRMRecordsFiltered(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string, filters []query.Clause, filterDesc string) (ragcore.AskResult, error) {
	return countGranted(grants, keys, filterDesc, func(key, scope string) (int, error) {
		n, err := store.CountRecordsFiltered(ctx, pool, key, scope, actorIdentityID, filters)
		if err != nil {
			return 0, fmt.Errorf("count filtered %s records: %w", key, err)
		}
		return n, nil
	})
}
