package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
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
	// Priority lists field keys (from Core or Custom) rendered first, in this
	// order, before the alphabetical rest — e.g. record number, name, status.
	// Retrieval truncates grounding text to a byte budget, so what comes
	// first is what the model reliably sees. Keys not present are skipped.
	Priority []string
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
	done := map[string]bool{}
	for _, k := range d.Priority {
		if done[k] {
			continue
		}
		if v, ok := d.Core[k]; ok {
			writeField(&b, k, v, nil)
			done[k] = true
		} else if v, ok := d.Custom[k]; ok {
			writeField(&b, k, v, d.FieldLabels)
			done[k] = true
		}
	}
	writeFields(&b, d.Core, nil, done)
	writeFields(&b, d.Custom, d.FieldLabels, done)
	return b.String()
}

// writeFields appends "Label: value" lines in sorted key order, skipping keys
// already rendered. labels may be nil; a key absent from it falls back to
// humanizeKey.
func writeFields(b *strings.Builder, fields map[string]any, labels map[string]string, skip map[string]bool) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeField(b, k, fields[k], labels)
	}
}

// writeField appends one "Label: value" line. Empty values (nil, "") are
// omitted: "Phone: <nil>" is noise that costs grounding budget and can read to
// a small model as a literal value. Nested maps/slices render as JSON rather
// than Go's map[...] syntax.
func writeField(b *strings.Builder, key string, v any, labels map[string]string) {
	text, ok := formatValue(v)
	if !ok {
		return
	}
	label, found := labels[key]
	if !found {
		label = humanizeKey(key)
	}
	fmt.Fprintf(b, "%s: %s\n", label, text)
}

func formatValue(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, strings.TrimSpace(x) != ""
	case map[string]any, []any:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x), true
		}
		return string(raw), true
	default:
		return fmt.Sprintf("%v", x), true
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
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// ContentHash returns the hex SHA-256 of s. Prefer VectorHash for anything
// deciding whether a stored embedding is still valid — see the note there.
func ContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// VectorHash identifies what a stored embedding was actually derived from: the
// rendered text AND the embedder that produced it.
//
// Hashing the content alone is not enough, and the failure is silent. Swapping
// the embedding model — or merely correcting its task prefix — changes the
// vector space while leaving the record's text byte-identical. A content-only
// hash would therefore report "unchanged" for every record after such a swap,
// so the reindex meant to rebuild the index would skip all of it and leave
// vectors in place that incoming queries can no longer be compared against.
// Folding the embedder's fingerprint in makes a model change invalidate every
// hash automatically, so the reindex does what its name says.
func VectorHash(embedderFingerprint, content string) string {
	// NUL-separated so a fingerprint/content pair can't collide with a
	// different split of the same concatenated bytes.
	return ContentHash(embedderFingerprint + "\x00" + content)
}
