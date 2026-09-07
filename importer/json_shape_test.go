package importer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These lock in Row/Summary's JSON shape as camelCase, matching the rest of
// the API (e.g. importJobSummary in controllers/import.go) — both types
// previously had no json tags at all and serialized with capitalized Go
// field names.
func TestRowMarshalsCamelCase(t *testing.T) {
	row := Row{
		ID: "row-1", JobID: "job-1", RowIndex: 0,
		Raw:      json.RawMessage(`{}`),
		Mapped:   MappedFields{Core: map[string]any{}, Custom: map[string]any{}},
		Errors:   []string{},
		Status:   StatusPending,
		RecordID: "",
	}
	body, err := json.Marshal(row)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	assert.ElementsMatch(t,
		[]string{"id", "jobId", "rowIndex", "raw", "mapped", "errors", "status", "recordId"},
		keysOf(out))
}

func TestSummaryMarshalsCamelCase(t *testing.T) {
	body, err := json.Marshal(Summary{Committed: 1, Failed: 2, Skipped: 3})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	assert.ElementsMatch(t, []string{"committed", "failed", "skipped"}, keysOf(out))
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
