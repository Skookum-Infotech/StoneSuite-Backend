// Package docs embeds this directory's markdown files into the binary at
// compile time, so app-help ingestion (ai/helpdocs, via
// POST /api/platform/ai/reindex-help) doesn't need the source tree present
// at runtime — see docs/superpowers/specs/2026-07-04-rag-ingest-help-design.md.
package docs

import "embed"

// FS is the curated app-help corpus — an explicit manifest, deliberately not a
// `*.md` glob.
//
// The glob swept in every markdown file that happened to live here, including
// generated API reference (~53 KB) and SAML setup (~40 KB). Retrieval pulls
// only helpRetrievalK=2 help chunks per question, so that bulk competed for —
// and won — slots that belong to the handful of documents actually written for
// end users. Operator and generated docs stay in the repo; they just do not
// belong in an end-user assistant's corpus.
//
// Add a file here only if a user might reasonably ask the assistant about it.
// Embedded files must be end-user help — screens, menus, buttons, and
// concepts a business user would ask about — never engineering docs (API
// paths, table names, internal services, architecture). ai-assistant.md is an
// engineering doc describing the assistant's own internals, so it stays in
// the repo but is deliberately not embedded here.
//
//go:embed crm-concepts.md
//go:embed assistant.md
//go:embed getting-started.md
//go:embed sales-documents.md
//go:embed purchasing.md
//go:embed inventory.md
//go:embed fabrication.md
//go:embed accounting.md
//go:embed import.md
//go:embed customer-portal.md
//go:embed administration.md
//go:embed feedback.md
var FS embed.FS
