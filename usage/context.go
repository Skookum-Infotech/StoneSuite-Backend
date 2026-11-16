package usage

import (
	"context"

	"stonesuite-backend/tenancy"
)

// Source names what kind of call consumed tokens — the per-source breakdown
// on the Subscription page.
type Source string

// The sources a tenant is billed for, plus SourceWarmup, which is never billed.
const (
	SourceAsk             Source = "ask"
	SourceRoute           Source = "route"
	SourceReportInterpret Source = "report_interpret"
	SourceDocExtract      Source = "doc_extract"
	SourceImport          Source = "import"
	// SourceOther is recorded for a tenant call no caller tagged.
	SourceOther Source = "other"
	// SourceWarmup marks platform-initiated model warm-ups; never recorded.
	SourceWarmup Source = "warmup"
)

type ctxKey int

const (
	tenantKey ctxKey = iota
	sourceKey
)

// WithTenant marks ctx as working on behalf of tenantID. Background jobs that
// run outside a request (document extraction, import) must call it; request
// paths get the tenant from tenancy's resolver automatically.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantKey, tenantID)
}

// TenantFrom returns the tenant ctx works for: an explicit WithTenant first,
// then the request's resolved tenant. "" means no tenant (platform work).
func TenantFrom(ctx context.Context) string {
	if id, ok := ctx.Value(tenantKey).(string); ok && id != "" {
		return id
	}
	if t, err := tenancy.TenantFromContext(ctx); err == nil {
		return t.ID
	}
	return ""
}

// WithSource tags the LLM calls made under ctx with source.
func WithSource(ctx context.Context, source Source) context.Context {
	return context.WithValue(ctx, sourceKey, source)
}

// SourceFrom returns ctx's source tag, or SourceOther when none was set.
func SourceFrom(ctx context.Context) Source {
	if s, ok := ctx.Value(sourceKey).(Source); ok && s != "" {
		return s
	}
	return SourceOther
}
