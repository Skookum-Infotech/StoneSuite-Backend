package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
)

// countCRMRecords sums CountRecords across keys, building a deterministic
// answer with zero LLM calls — a plain count needs no generation, and skipping
// the chat model avoids both its latency and any chance of it mis-stating the
// number. Citations are always an empty (never nil) slice, matching
// ragcore.AskResult's existing JSON convention.
func countCRMRecords(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string) (ragcore.AskResult, error) {
	return countGranted(grants, keys, "", func(key, scope string) (int, error) {
		n, err := store.CountRecords(ctx, pool, key, scope, actorIdentityID)
		if err != nil {
			return 0, fmt.Errorf("count %s records: %w", key, err)
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
		return fmt.Sprintf("You have %d %s%s.", counts[keys[0]], pluralize(keys[0], counts[keys[0]]), suffix)
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[key], pluralize(key, counts[key])))
	}
	return fmt.Sprintf("You have %s%s (%d CRM records total).", strings.Join(parts, ", "), suffix, total)
}

// pluralize returns key ("lead"/"prospect"/"customer") pluralized for n.
func pluralize(key string, n int) string {
	if n == 1 {
		return key
	}
	return key + "s"
}
