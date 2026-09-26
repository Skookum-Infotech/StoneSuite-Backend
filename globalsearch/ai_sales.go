package globalsearch

import (
	"context"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/quote"
)

// aiPriorityDocument is the shared Priority order for document modules: what a
// question most often names, rendered first under the byte budget.
var aiPriorityDocument = []string{"number", "status", "customer", "date", "grand_total", "balance"}

var _ = addAI("quote", AIHooks{
	OwnScoped: true,
	Label:     "quote",
	Load:      loadQuoteAI,
	Count: func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error) {
		return countTable(ctx, pool, scope, identityID, "quote", "quote_deleted_at", "quote_owner_id")
	},
	ListLive: func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error) {
		return liveTable(ctx, pool, "quote", "quote_uuid", "quote_updated_at", "quote_deleted_at")
	},
})

func loadQuoteAI(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error) {
	q, err := quote.Get(ctx, pool, id)
	if err != nil {
		return AIRecord{}, err
	}
	lines := make([]aiLine, len(q.Items))
	for i, it := range q.Items {
		lines[i] = aiLine{Name: it.ItemName, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, Total: it.LineTotal}
	}
	core := map[string]any{
		"number":      q.Number,
		"status":      q.Status,
		"customer":    q.Customer.Name,
		"date":        q.QuoteDate,
		"valid_until": q.ValidUntil,
		"po_number":   q.PONumber,
		"memo":        q.Memo,
		"subtotal":    q.Subtotal,
		"tax_total":   q.TaxTotal,
		"grand_total": q.GrandTotal,
		"line_items":  summarizeLines(lines),
	}
	return AIRecord{
		Doc:         rag.RecordDoc{WorkflowKey: "quote", StateName: q.Status, Core: core, Priority: aiPriorityDocument},
		OwnerUserID: q.OwnerUserID,
	}, nil
}
