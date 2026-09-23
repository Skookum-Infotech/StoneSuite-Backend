// Package helpdocs chunks and ingests app-help markdown docs into the
// control-plane's cp_rag_chunks table. Used by both the rag-ingest-help CLI
// (local dev) and the POST /api/platform/ai/reindex-help handler (prod).
package ingest

import (
	"regexp"
	"strings"
)

// Section is one heading-delimited chunk of a markdown document.
type Section struct {
	Title   string
	Content string
}

var headingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)

// ChunkMarkdown splits markdown text into sections at each heading line
// ("#".."######"). Each section's Content starts at its heading line and
// runs to (but not including) the next heading. Text before the first
// heading becomes its own section titled fallbackTitle (an intro paragraph is
// often the most useful part of a doc), and a document with no headings at
// all becomes one section titled fallbackTitle. Lines inside fenced code
// blocks are never treated as headings — a "# comment" in a shell snippet is
// not a section. An empty document produces no sections.
func ChunkMarkdown(text, fallbackTitle string) []Section {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")

	var sections []Section
	curTitle := fallbackTitle
	var curLines []string
	inFence := false

	flush := func() {
		content := strings.TrimSpace(strings.Join(curLines, "\n"))
		if content != "" {
			sections = append(sections, Section{Title: curTitle, Content: content})
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence {
			if m := headingRe.FindStringSubmatch(line); m != nil {
				flush()
				curTitle = m[1]
				curLines = []string{line}
				continue
			}
		}
		curLines = append(curLines, line)
	}
	flush()
	return sections
}
