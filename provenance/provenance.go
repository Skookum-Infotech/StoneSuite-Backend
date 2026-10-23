// Package provenance records, on a context, which channel caused a write
// (a human, the automation engine, the AI agent, ...). It is a leaf package.
package provenance

import "context"

// Via names the channel a write arrived through.
type Via string

const (
	// User is a direct human action; it is the default when nothing is stamped.
	User Via = "user"
	// Automation is the date-triggered automation engine.
	Automation Via = "automation"
	// AIAgent is the AI assistant acting on a user's confirmed proposal.
	AIAgent Via = "ai_agent"
	// DocExtract is the document-extraction flow.
	DocExtract Via = "docextract"
)

type ctxKey struct{}

// With returns a copy of ctx stamped with the given provenance.
func With(ctx context.Context, v Via) context.Context {
	return context.WithValue(ctx, ctxKey{}, v)
}

// From returns the provenance stamped on ctx, or User when none is set.
func From(ctx context.Context) Via {
	if v, ok := ctx.Value(ctxKey{}).(Via); ok && v != "" {
		return v
	}
	return User
}
