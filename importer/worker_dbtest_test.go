//go:build dbtest

package importer

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/jobqueue"
	"stonesuite-backend/workflow"
)

// newTestQueue connects to the control-plane test database — UpdateProgress
// (the only Queue method stageTabular/stageExtracted call) is a plain UPDATE
// keyed by job id and never errors when no row matches, so these tests need
// a real Queue to satisfy Worker's concrete field type but no fixture job row.
func newTestQueue(t *testing.T) *jobqueue.Queue {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test cp db: %v", err)
	}
	t.Cleanup(pool.Close)
	return jobqueue.New(pool)
}

func TestWorker_StageTabularInsertsOneRowPerTableRow(t *testing.T) {
	pool := newTestPool(t)
	queue := newTestQueue(t)
	w := NewWorker(nil, nil, queue, nil, nil)
	rows := NewStore(pool)
	ctx := context.Background()

	defs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber, Required: true}}
	mapping := map[string]string{"Name": "core:name", "Budget": "cf:budget"}
	tableRows := []map[string]string{
		{"Name": "Acme", "Budget": "5000"},
		{"Name": "Globex", "Budget": "not-a-number"}, // present but the wrong type -> staged with an error hint
	}

	err := w.stageTabular(ctx, rows, "job-tabular-1", tableRows, mapping, defs)
	require.NoError(t, err)

	staged, err := rows.ListByJob(ctx, "job-tabular-1")
	require.NoError(t, err)
	require.Len(t, staged, 2)

	require.Equal(t, 0, staged[0].RowIndex)
	require.Equal(t, map[string]any{"name": "Acme"}, staged[0].Mapped.Core)
	require.Equal(t, map[string]any{"budget": 5000.0}, staged[0].Mapped.Custom,
		"a number field's cell text must be coerced to an actual number, not left as a string")
	require.Empty(t, staged[0].Errors)
	require.Equal(t, StatusPending, staged[0].Status)

	require.Equal(t, 1, staged[1].RowIndex)
	require.Equal(t, map[string]any{"budget": "not-a-number"}, staged[1].Mapped.Custom,
		"a value that fails to coerce must fall back to the raw string so validation reports its own error")
	require.NotEmpty(t, staged[1].Errors, "a present but wrong-type custom field must surface a staging hint")
}

func TestWorker_StageExtractedInsertsOneRecordFromWholeDocument(t *testing.T) {
	pool := newTestPool(t)
	queue := newTestQueue(t)
	llm := &fakeStructuredLLM{reply: `{"budget": 7500}`}
	w := NewWorker(nil, nil, queue, nil, llm)
	rows := NewStore(pool)
	ctx := context.Background()

	defs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber}}

	err := w.stageExtracted(ctx, rows, "job-extracted-1", "Budget: 7500 dollars.", defs)
	require.NoError(t, err)

	staged, err := rows.ListByJob(ctx, "job-extracted-1")
	require.NoError(t, err)
	require.Len(t, staged, 1, "a free-form document must stage exactly one candidate record")

	require.Equal(t, map[string]any{"budget": 7500.0}, staged[0].Mapped.Custom)
	require.Empty(t, staged[0].Mapped.Core, "DOCX/PDF extraction never populates core fields")

	var raw map[string]string
	require.NoError(t, json.Unmarshal(staged[0].Raw, &raw))
	require.Equal(t, "Budget: 7500 dollars.", raw["text"])
}

func TestWorker_StageExtractedPropagatesUnsupportedLLM(t *testing.T) {
	pool := newTestPool(t)
	queue := newTestQueue(t)
	w := NewWorker(nil, nil, queue, nil, plainLLM{}) // no ChatJSON -> ErrExtractionUnsupported
	rows := NewStore(pool)
	ctx := context.Background()

	defs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber}}
	err := w.stageExtracted(ctx, rows, "job-extracted-2", "some text", defs)
	require.Error(t, err)

	staged, lerr := rows.ListByJob(ctx, "job-extracted-2")
	require.NoError(t, lerr)
	require.Empty(t, staged, "a failed extraction must not leave a partial staged row behind")
}

// compile-time reminder that stageTabular/stageExtracted never touch these
// Worker fields, which is what makes passing nil for them here safe.
var _ ragcore.LLMClient = plainLLM{}
