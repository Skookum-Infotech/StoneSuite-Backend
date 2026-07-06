package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// RecordDoc is the store-agnostic view of a workflow record that RenderRecord
// flattens into embeddable text. Callers (the ingestion worker) map their own
// record shape onto this so the ai package stays decoupled from workflow/crmstore.
type RecordDoc struct {
	WorkflowKey string
	StateName   string
	Core        map[string]any
	Custom      map[string]any
	// FieldLabels maps a Custom field's key (e.g. "deal_size") to its
	// admin-defined display label (e.g. "Deal Size ($)"), from
	// workflow_field_definitions. Optional: a key with no entry renders under
	// its raw key (see humanizeKey) rather than being dropped.
	FieldLabels map[string]string
}

// RenderRecord flattens a record into a stable, human-readable text block for
// embedding. Field order is sorted so the output (and thus its ContentHash) is
// deterministic regardless of map iteration order. Human-readable field names
// (admin-defined labels for Custom fields, title-cased keys otherwise) help
// both embedding recall and a small chat model's comprehension — a raw key
// like "co_nm" reads far worse than "Company Name".
func RenderRecord(d RecordDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workflow: %s\n", d.WorkflowKey)
	fmt.Fprintf(&b, "State: %s\n", d.StateName)
	writeFields(&b, d.Core, nil)
	writeFields(&b, d.Custom, d.FieldLabels)
	return b.String()
}

// writeFields appends "Label: value" lines in sorted key order. labels may be
// nil; a key absent from it (or when labels itself is nil) falls back to
// humanizeKey.
func writeFields(b *strings.Builder, fields map[string]any, labels map[string]string) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		label, ok := labels[k]
		if !ok {
			label = humanizeKey(k)
		}
		fmt.Fprintf(b, "%s: %v\n", label, fields[k])
	}
}

// humanizeKey turns a snake_case or camelCase field key into a Title Case
// display label (e.g. "company_name" or "companyName" -> "Company Name") for
// keys with no admin-defined label to fall back on.
func humanizeKey(k string) string {
	k = strings.ReplaceAll(k, "_", " ")
	var spaced strings.Builder
	runes := []rune(k)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' && runes[i-1] != ' ' {
			spaced.WriteByte(' ')
		}
		spaced.WriteRune(r)
	}
	words := strings.Fields(spaced.String())
	for i, w := range words {
		lower := strings.ToLower(w)
		words[i] = strings.ToUpper(lower[:1]) + lower[1:]
	}
	return strings.Join(words, " ")
}

// ContentHash returns the hex SHA-256 of s, used to skip re-embedding unchanged
// record text and protect the free-tier embedding quota.
func ContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
