package globalsearch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// AIRecord is one module row in the form the RAG index stores: the renderable
// document plus the owner column retrieval scopes "own" grants on.
type AIRecord struct {
	Doc         rag.RecordDoc
	OwnerUserID string // tenant users.id of the record owner; "" when unowned
}

// AILoadFunc loads one live record by its external uuid. It must return the
// module's own not-found error for a missing or soft-deleted row so the index
// worker treats the job as stale.
type AILoadFunc func(ctx context.Context, pool *pgxpool.Pool, id string) (AIRecord, error)

// AIListLiveFunc returns every live (not soft-deleted) record's external uuid
// mapped to its updated_at, for the reconciliation sweep.
type AIListLiveFunc func(ctx context.Context, pool *pgxpool.Pool) (map[string]time.Time, error)

// AICountFunc counts the module's live rows the caller may read under scope.
// A scope other than "all" narrows to the caller's own rows (fail-closed), the
// same rule the module's Search applies.
type AICountFunc func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (int, error)

// AISumFunc sums one numeric field across the module's live rows the caller
// may read under scope (same fail-closed narrowing as AICountFunc), returning
// both the total and how many rows contributed to it — n lets a caller
// distinguish "nothing outstanding" from "nothing to read at all", and render
// "$X across N records" without a second query. A real SQL SUM, computed
// against every matching row, never an LLM asked to add up whatever a RAG
// retrieval happened to surface.
type AISumFunc func(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID string) (total float64, n int, err error)

// AIHooks makes a registered module answerable by the assistant. A provider
// with no entry in the AI registry is simply not indexed (the assistant then
// says so, rather than answering from unrelated records).
type AIHooks struct {
	// RecordType is rag_chunks.record_type for this module and the key the
	// caller's grants are held under. Defaults to the provider Key.
	RecordType string
	// AuditResource is the audit_logs.resource value this module writes, which
	// triggers re-indexing (workflow.LogAuditFull). Defaults to the provider Key.
	AuditResource string
	// OwnScoped is true when the module's list honours an "own" scope. Modules
	// without an owner column (inventory, chart of accounts) see everything on
	// any read grant, so their grant is always treated as "all".
	OwnScoped bool
	// Label is the singular display name used in answers ("sales order").
	Label string
	// Nouns are the lowercase singular words a question uses for this module
	// ("sales order", "so"), each also matched with a trailing "s". The Label is
	// always included.
	Nouns    []string
	Load     AILoadFunc
	ListLive AIListLiveFunc
	Count    AICountFunc
	// Sum is optional — nil for a module with no aggregate-able numeric field
	// the assistant answers "total X" questions about. See AISumFunc.
	Sum AISumFunc
}

// aiRegistry holds the AI hooks by provider Key. It is separate from registry
// so the per-module files need no init-order dependency on providers_*.go.
var aiRegistry = map[string]AIHooks{}

// addAI registers a module's AI hooks, defaulting RecordType and AuditResource
// to key, and returns them for `var _ = addAI(...)` package-init use.
func addAI(key string, h AIHooks) AIHooks {
	if _, dup := aiRegistry[key]; dup {
		panic("globalsearch: duplicate AI hooks for " + key)
	}
	if h.RecordType == "" {
		h.RecordType = key
	}
	if h.AuditResource == "" {
		h.AuditResource = key
	}
	if h.Label == "" {
		h.Label = strings.ReplaceAll(key, "_", " ")
	}
	h.Nouns = append([]string{h.Label}, h.Nouns...)
	aiRegistry[key] = h
	return h
}

// AIProvider is a registered provider that also has AI hooks.
type AIProvider struct {
	Provider
	AI AIHooks
}

// AIProviders returns every AI-indexed module, sorted by key.
func AIProviders() []AIProvider {
	out := make([]AIProvider, 0, len(aiRegistry))
	for key, h := range aiRegistry {
		if p, ok := registry[key]; ok {
			out = append(out, AIProvider{Provider: p, AI: h})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// AIByRecordType finds the AI module owning rag_chunks.record_type t.
func AIByRecordType(t string) (AIProvider, bool) {
	for _, p := range AIProviders() {
		if p.AI.RecordType == t {
			return p, true
		}
	}
	return AIProvider{}, false
}

// AIByAuditResource finds the AI module whose writes audit as resource r.
func AIByAuditResource(r string) (AIProvider, bool) {
	for _, p := range AIProviders() {
		if p.AI.AuditResource == r {
			return p, true
		}
	}
	return AIProvider{}, false
}

// liveTable lists live rows of one module table for AIListLiveFunc. Every
// argument is a compile-time constant from the module's own hook file, never
// client input, so interpolating the identifiers is safe.
func liveTable(ctx context.Context, pool *pgxpool.Pool, table, uuidCol, updatedCol, deletedCol string) (map[string]time.Time, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(
		`SELECT %s::text, %s FROM %s WHERE %s IS NULL`, uuidCol, updatedCol, table, deletedCol))
	if err != nil {
		return nil, fmt.Errorf("list live %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("scan live %s: %w", table, err)
		}
		out[id] = at
	}
	return out, rows.Err()
}

// employeeIDByIdentity resolves a control-plane identity to its tenant
// employee id, the owner column the document modules scope "own" reads on.
func employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
	if identityID == "" {
		return 0, false
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// countTable counts live rows of one module table for AICountFunc. ownerCol is
// the employee-id owner column, or "" for a module with no owner narrowing.
// Identifiers are constants from the module's hook file (see liveTable); the
// employee id is a bound parameter.
func countTable(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, table, deletedCol, ownerCol string) (int, error) {
	q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s IS NULL`, table, deletedCol)
	var args []any
	if ownerCol != "" && scope != authz.ScopeAll {
		empID, found := employeeIDByIdentity(ctx, pool, identityID)
		if !found {
			return 0, nil
		}
		q += fmt.Sprintf(` AND %s = $1`, ownerCol)
		args = append(args, empID)
	}
	var n int
	if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// sumTable sums one numeric column of live, non-deleted rows matching cond
// (e.g. "balance_due > 0" — the module's own definition of "outstanding",
// passed in rather than hardcoded here since it varies per module) for
// AISumFunc. ownerCol/scope narrowing mirrors countTable exactly. Identifiers
// (table, column, deletedCol, ownerCol, cond) are compile-time constants from
// the module's own hook file, never client input, so interpolating them is
// safe — the same contract countTable/liveTable already rely on.
func sumTable(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, identityID, table, column, deletedCol, ownerCol, cond string) (float64, int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(SUM(%s), 0), COUNT(*) FROM %s WHERE %s IS NULL AND %s`, column, table, deletedCol, cond)
	var args []any
	if ownerCol != "" && scope != authz.ScopeAll {
		empID, found := employeeIDByIdentity(ctx, pool, identityID)
		if !found {
			return 0, 0, nil
		}
		q += fmt.Sprintf(` AND %s = $1`, ownerCol)
		args = append(args, empID)
	}
	var total float64
	var n int
	if err := pool.QueryRow(ctx, q, args...).Scan(&total, &n); err != nil {
		return 0, 0, fmt.Errorf("sum %s.%s: %w", table, column, err)
	}
	return total, n, nil
}

// maxAILineItems caps how many line items a summary spells out, keeping a
// document within the embedder's token budget.
const maxAILineItems = 5

// aiLine is one document line item in the shape summarizeLines renders.
type aiLine struct {
	Name      string
	SKU       string
	Quantity  float64
	UnitPrice float64
	Total     float64
}

// summarizeLines renders up to maxAILineItems lines as one Core value, e.g.
// "1. Granite slab (GR-01) x2 @ 450.00 = 900.00; ... (+3 more)".
func summarizeLines(lines []aiLine) string {
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, 0, maxAILineItems+1)
	for i, l := range lines {
		if i == maxAILineItems {
			parts = append(parts, fmt.Sprintf("(+%d more)", len(lines)-maxAILineItems))
			break
		}
		name := l.Name
		if l.SKU != "" {
			name += " (" + l.SKU + ")"
		}
		parts = append(parts, fmt.Sprintf("%d. %s x%g @ %.2f = %.2f", i+1, name, l.Quantity, l.UnitPrice, l.Total))
	}
	return strings.Join(parts, "; ")
}
