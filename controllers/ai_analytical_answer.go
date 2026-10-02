package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/globalsearch"
	"stonesuite-backend/query"
)

// countCRMRecords sums the per-type count (CRM store or module hook) across keys, building a deterministic
// answer with zero LLM calls — a plain count needs no generation, and skipping
// the chat model avoids both its latency and any chance of it mis-stating the
// number. Citations are always an empty (never nil) slice, matching
// ragcore.AskResult's existing JSON convention.
func countCRMRecords(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string) (ragcore.AskResult, error) {
	return countGranted(grants, keys, "", func(key, scope string) (int, error) {
		return countRecordType(ctx, store, pool, actorIdentityID, key, scope)
	})
}

// openClosedFilterDesc renders classifyOpenClosedCount's answer suffix, e.g.
// "You have 3 leads with an open status."
func openClosedFilterDesc(open bool) string {
	if open {
		return "an open status"
	}
	return "a closed status"
}

// countCRMRecordsOpenClosed answers a classifyOpenClosedCount match: for each
// key, resolves its own workflow's terminal/non-terminal status set via
// store.Statuses (workflow.StatusInfo.IsTerminal — the same flag the workflow
// engine tracks per status, populated correctly under either tenant design
// version, see crmstore.Store) and counts records whose status is IN that
// set, via the existing filter engine (query.OpIn) — never a guess at a
// literal status name. A workflow with no status of the requested polarity
// (e.g. every status is terminal) counts as zero rather than an error.
func countCRMRecordsOpenClosed(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string, open bool) (ragcore.AskResult, error) {
	return countGranted(grants, keys, openClosedFilterDesc(open), func(key, scope string) (int, error) {
		statuses, err := store.Statuses(ctx, pool, key)
		if err != nil {
			return 0, fmt.Errorf("statuses %s: %w", key, err)
		}
		ids := make([]any, 0, len(statuses))
		for _, s := range statuses {
			if s.IsTerminal == !open {
				ids = append(ids, s.StateID)
			}
		}
		if len(ids) == 0 {
			return 0, nil
		}
		n, err := store.CountRecordsFiltered(ctx, pool, key, scope, actorIdentityID, []query.Clause{{Field: "status", Op: query.OpIn, Value: ids}})
		if err != nil {
			return 0, fmt.Errorf("count open/closed %s records: %w", key, err)
		}
		return n, nil
	})
}

// countGranted counts each requested key the caller can read under THAT key's
// own scope — a customer:own grant must not narrow a lead:all count, nor a
// lead:all grant widen a customer count — and names the keys it left out
// instead of counting them. A question only about ungranted types gets the
// no-access sentence and no number at all. filterDesc (see describeFilters)
// is "" for a plain count and stated in the answer for a routed one.
func countGranted(grants ai.Grants, keys []string, filterDesc string, count func(key, scope string) (int, error)) (ragcore.AskResult, error) {
	granted, denied := splitGranted(grants, keys)
	if len(granted) == 0 {
		return ragcore.AskResult{Answer: noAccessSentence(denied), Citations: []ragcore.Citation{}}, nil
	}
	counts := make(map[string]int, len(granted))
	total := 0
	for _, key := range granted {
		n, err := count(key, grants[key])
		if err != nil {
			return ragcore.AskResult{}, err
		}
		counts[key] = n
		total += n
	}
	answer := formatCountAnswer(granted, counts, total, filterDesc)
	if len(denied) > 0 {
		answer += " " + noAccessSentence(denied)
	}
	return ragcore.AskResult{Answer: answer, Citations: []ragcore.Citation{}}, nil
}

// formatCountAnswer renders counts as a plain sentence. A single key renders
// as "You have N <key>s[ with <filterDesc>]." (pluralizing the CRM type
// word); multiple keys (the "how many CRM records" case) list each type plus
// a total. filterDesc is "" for an unfiltered count.
func formatCountAnswer(keys []string, counts map[string]int, total int, filterDesc string) string {
	suffix := ""
	if filterDesc != "" {
		suffix = " with " + filterDesc
	}
	if len(keys) == 1 {
		return fmt.Sprintf("You have %d %s%s.", counts[keys[0]], pluralize(recordTypeLabel(keys[0]), counts[keys[0]]), suffix)
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[key], pluralize(recordTypeLabel(key), counts[key])))
	}
	scopeWord := "CRM records"
	if anyModuleType(keys) {
		scopeWord = "records"
	}
	return fmt.Sprintf("You have %s%s (%d %s total).", strings.Join(parts, ", "), suffix, total, scopeWord)
}

// pluralize returns key ("lead"/"prospect"/"customer") pluralized for n.
func pluralize(key string, n int) string {
	if n == 1 {
		return key
	}
	return key + "s"
}

// sumModuleRecords answers a classifySumQuestion match via the module's own
// AI Sum hook (see globalsearch.AISumFunc) — a real SQL SUM computed under
// the caller's own scope, zero LLM calls. Mirrors countRecordType's
// scope/grant handling (ai_modules.go) rather than reusing it directly: a sum
// has no CRM-store counterpart to dispatch to, only ever a module hook.
func sumModuleRecords(ctx context.Context, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID, key string) (ragcore.AskResult, error) {
	scope, granted := grants[key]
	if !granted {
		return ragcore.AskResult{Answer: noAccessSentence([]string{recordTypeLabel(key)}), Citations: []ragcore.Citation{}}, nil
	}
	p, ok := globalsearch.AIByRecordType(key)
	if !ok || p.AI.Sum == nil {
		return ragcore.AskResult{}, fmt.Errorf("sum %s: no AI sum hook", key)
	}
	total, n, err := p.AI.Sum(ctx, pool, authz.Scope(scope), actorIdentityID)
	if err != nil {
		return ragcore.AskResult{}, fmt.Errorf("sum %s records: %w", key, err)
	}
	label := recordTypeLabel(key)
	if n == 0 {
		return ragcore.AskResult{Answer: fmt.Sprintf("No %s have an outstanding balance.", pluralize(label, 0)), Citations: []ragcore.Citation{}}, nil
	}
	return ragcore.AskResult{
		Answer:    fmt.Sprintf("$%.2f outstanding across %d %s.", total, n, pluralize(label, n)),
		Citations: []ragcore.Citation{},
	}, nil
}
