package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai/index"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/globalsearch"
	"stonesuite-backend/workflow"
)

// ragLoaderRouter is the index worker's RecordLoader: a job with no record type
// is a CRM workflow record; anything else is a module row loaded through that
// module's globalsearch AI hooks.
type ragLoaderRouter struct {
	crm  *crmstore.RAGRecordLoader
	pool *pgxpool.Pool
}

// newRAGLoaderRouter builds the router over one tenant's CRM loader and pool.
func newRAGLoaderRouter(crm *crmstore.RAGRecordLoader, pool *pgxpool.Pool) *ragLoaderRouter {
	return &ragLoaderRouter{crm: crm, pool: pool}
}

// Load implements index.RecordLoader.
func (r *ragLoaderRouter) Load(ctx context.Context, recordType, sourceID string) (rag.RecordDoc, string, string, string, error) {
	if recordType == "" {
		return r.crm.Load(ctx, sourceID)
	}
	p, ok := globalsearch.AIByRecordType(recordType)
	if !ok {
		return rag.RecordDoc{}, "", "", "", fmt.Errorf("no AI loader for record type %q", recordType)
	}
	rec, err := p.AI.Load(ctx, r.pool, sourceID)
	if err != nil {
		return rag.RecordDoc{}, "", "", "", fmt.Errorf("load %s %s: %w", recordType, sourceID, err)
	}
	return rec.Doc, "", rec.OwnerUserID, "", nil
}

// ragAuditObserver re-indexes an AI module's record whenever its controller
// audits a write (workflow.LogAuditFull): a delete drops the vector, anything
// else refreshes it. Best-effort — reconciliation is the correctness backstop.
func ragAuditObserver(ctx context.Context, q workflow.Querier, action, resource, resourceID string) {
	p, ok := globalsearch.AIByAuditResource(resource)
	if !ok || resourceID == "" {
		return
	}
	op := "upsert"
	if action == "delete" {
		op = "delete"
	}
	if err := index.EnqueueVia(ctx, q, resourceID, p.AI.RecordType, op); err != nil {
		slog.Warn("rag index enqueue failed", "record_type", p.AI.RecordType, "source_id", resourceID, "op", op, "err", err)
	}
}
