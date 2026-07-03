package crmstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
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
	}
	return doc, rec.WorkflowID, rec.OwnerUserID, rec.TeamID, nil
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
