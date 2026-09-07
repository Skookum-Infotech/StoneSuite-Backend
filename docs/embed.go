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
//
//go:embed ai-assistant.md
//go:embed crm-concepts.md
var FS embed.FS
