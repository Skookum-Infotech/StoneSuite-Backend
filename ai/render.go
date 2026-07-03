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
}

// RenderRecord flattens a record into a stable, human-readable text block for
// embedding. Field order is sorted so the output (and thus its ContentHash) is
// deterministic regardless of map iteration order.
func RenderRecord(d RecordDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workflow: %s\n", d.WorkflowKey)
	fmt.Fprintf(&b, "State: %s\n", d.StateName)
	writeFields(&b, d.Core)
	writeFields(&b, d.Custom)
	return b.String()
}

// writeFields appends "key: value" lines in sorted key order.
func writeFields(b *strings.Builder, fields map[string]any) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s: %v\n", k, fields[k])
	}
}

// ContentHash returns the hex SHA-256 of s, used to skip re-embedding unchanged
// record text and protect the free-tier embedding quota.
func ContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
