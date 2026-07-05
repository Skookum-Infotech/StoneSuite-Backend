// Package docs embeds this directory's markdown files into the binary at
// compile time, so app-help ingestion (ai/helpdocs, via
// POST /api/platform/ai/reindex-help) doesn't need the source tree present
// at runtime — see docs/superpowers/specs/2026-07-04-rag-ingest-help-design.md.
package docs

import "embed"

//go:embed *.md
var FS embed.FS
