package crmstore

import (
	"fmt"
	"sort"
	"strings"
)

// ragRecordByteBudget caps a record's rendered text before it's embedded —
// see capRecordFields. Every RAG question today sends 4 records + 2 help
// chunks at full field width, which is most of the ~2,100 tokens a question
// costs against a CPU-bound ~20-25 tok/s model. Trimming each record to a
// handful of the fields a question actually names cuts prefill without
// dropping the fields retrieval and answering most often need (see
// ragPriorityFields).
const ragRecordByteBudget = 700

// ragRecordHeaderOverheadBytes approximates the "Workflow: x\nState: y\n"
// header rag.RenderRecord always emits ahead of any field, which
// capRecordFields has no visibility into (it only sees Core/Custom). Reserved
// up front so the actual rendered record — header included — stays close to
// ragRecordByteBudget rather than a little over it.
const ragRecordHeaderOverheadBytes = 64

// capRecordFields returns copies of core/custom trimmed to fit within budget
// bytes of estimated rendered text: priority fields first (in the order
// rag.RenderRecord itself renders them), then the remaining Core fields
// alphabetically, then the remaining Custom fields alphabetically — stopping
// as soon as the next field would push the estimate over budget. Capping
// always drops a whole field, never truncates a value mid-string: a field
// the model can cite must read as what it actually says, not a fragment.
//
// The byte estimate is approximate (label length varies with fieldLabels,
// which capRecordFields doesn't reformat the way rag.RenderRecord's
// humanizeKey does) — close enough for a soft cap, not meant to be exact.
func capRecordFields(core, custom map[string]any, priority []string, fieldLabels map[string]string, budget int) (map[string]any, map[string]any) {
	remaining := budget - ragRecordHeaderOverheadBytes
	outCore := make(map[string]any, len(core))
	outCustom := make(map[string]any, len(custom))
	done := make(map[string]bool, len(priority)+len(core)+len(custom))
	full := false // once one field doesn't fit, stop adding any more

	tryAdd := func(key string, v any, fromCustom bool) {
		if done[key] || full {
			return
		}
		done[key] = true
		n, ok := ragFieldRenderCost(key, v, fieldLabels)
		if !ok {
			return // empty/nil fields cost nothing and render nothing
		}
		if n > remaining {
			full = true
			return
		}
		remaining -= n
		if fromCustom {
			outCustom[key] = v
		} else {
			outCore[key] = v
		}
	}

	for _, key := range priority {
		if v, ok := core[key]; ok {
			tryAdd(key, v, false)
			continue
		}
		if v, ok := custom[key]; ok {
			tryAdd(key, v, true)
		}
	}
	for _, key := range ragSortedKeys(core) {
		tryAdd(key, core[key], false)
	}
	for _, key := range ragSortedKeys(custom) {
		tryAdd(key, custom[key], true)
	}
	return outCore, outCustom
}

// ragFieldRenderCost estimates the byte length of one "Label: value\n" line
// rag.RenderRecord would emit for key/v, mirroring its own field-skip rule
// (a nil or blank-string value renders nothing, so it costs nothing). ok is
// false for exactly those skipped fields.
func ragFieldRenderCost(key string, v any, fieldLabels map[string]string) (n int, ok bool) {
	text, ok := ragFieldValueText(v)
	if !ok {
		return 0, false
	}
	label, found := fieldLabels[key]
	if !found {
		label = key
	}
	return len(label) + len(": ") + len(text) + len("\n"), true
}

// ragFieldValueText mirrors rag.RenderRecord's formatValue closely enough for
// a byte-cost estimate: a nil or blank string renders nothing (ok=false);
// everything else renders via fmt's default formatting, which is what
// RenderRecord falls back to for anything that isn't a string.
func ragFieldValueText(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, strings.TrimSpace(x) != ""
	default:
		return fmt.Sprintf("%v", x), true
	}
}

// ragSortedKeys returns m's keys sorted — the same deterministic order
// rag.RenderRecord itself iterates the non-priority fields in.
func ragSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
