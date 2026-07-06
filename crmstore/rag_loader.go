package crmstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/workflow"
)

// RAGRecordLoader adapts a Store to ai/index.RecordLoader, resolving a
// record's embeddable text + RBAC scope columns from its external id. It
// satisfies index.RecordLoader structurally (checked at the wiring call
// site), so crmstore never has to import ai/index.
type RAGRecordLoader struct {
	store Store
	pool  *pgxpool.Pool
}

// NewRAGRecordLoader builds a loader bound to one tenant's store + pool.
func NewRAGRecordLoader(store Store, pool *pgxpool.Pool) *RAGRecordLoader {
	return &RAGRecordLoader{store: store, pool: pool}
}

// Load resolves the record's workflow key + current state label into an
// ai.RecordDoc, alongside the scope columns (workflow id, owner, team) RAG
// retrieval will later AND the RBAC scope onto.
func (l *RAGRecordLoader) Load(ctx context.Context, sourceID string) (ai.RecordDoc, string, string, string, error) {
	rec, err := l.store.GetRecord(ctx, l.pool, sourceID)
	if err != nil {
		return ai.RecordDoc{}, "", "", "", fmt.Errorf("load record %s: %w", sourceID, err)
	}
	key, err := l.store.KeyForRecord(ctx, l.pool, sourceID)
	if err != nil {
		return ai.RecordDoc{}, "", "", "", fmt.Errorf("resolve workflow key for %s: %w", sourceID, err)
	}
	doc := ai.RecordDoc{
		WorkflowKey: key,
		StateName:   l.stateName(ctx, rec.CurrentStateID),
		Core:        rec.CoreFields,
		Custom:      rec.CustomFields,
		FieldLabels: l.fieldLabels(ctx, key),
	}
	return doc, workflowUUIDOrEmpty(rec.WorkflowID), rec.OwnerUserID, rec.TeamID, nil
}

// fieldLabels resolves key's (lead|prospect|customer) admin-defined custom
// field labels for RenderRecord — works for both the v1 dynamic-workflow
// store and v2 relational store, since GetWorkflowByKey resolves by the CRM
// type key, not a per-record workflow UUID (see workflowUUIDOrEmpty).
// Best-effort: no pool (test doubles construct a loader without one) or any
// lookup failure (no matching workflow, no fields defined) yields a nil map,
// so RenderRecord's humanizeKey fallback still produces a usable, if less
// precise, label rather than failing the whole index job.
func (l *RAGRecordLoader) fieldLabels(ctx context.Context, key string) map[string]string {
	if l.pool == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, l.pool, key)
	if err != nil {
		return nil
	}
	def, err := workflow.LoadDefinition(ctx, l.pool, wf.ID)
	if err != nil {
		return nil
	}
	labels := make(map[string]string, len(def.Fields))
	for _, f := range def.Fields {
		labels[f.Key] = f.Label
	}
	return labels
}

// workflowUUIDOrEmpty guards the boundary between two incompatible meanings
// of Record.WorkflowID: the v1 JSONB store sets it to a real workflows.id
// UUID, but the v2 relational store reuses the same field for a fixed
// type-key string (lead/prospect/customer — see relational_store.go). Only a
// genuine UUID may reach rag_chunks.workflow_id; anything else becomes empty
// (RagStore.Upsert maps that to SQL NULL).
func workflowUUIDOrEmpty(id string) string {
	var u pgtype.UUID
	if u.Scan(id) != nil {
		return ""
	}
	return id
}

// stateName resolves a state id to its human-readable label. Best-effort: a
// lookup failure returns an empty label rather than failing the whole index
// job — the record still gets a usable, if less precise, document.
func (l *RAGRecordLoader) stateName(ctx context.Context, stateID string) string {
	statuses, err := l.store.AllStatuses(ctx, l.pool)
	if err != nil {
		return ""
	}
	for _, s := range statuses {
		if s.StateID == stateID {
			return s.StatusLabel
		}
	}
	return ""
}
