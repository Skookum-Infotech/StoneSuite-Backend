package main

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
// runs to (but not including) the next heading. Content before the first
// heading is dropped, UNLESS the document has no headings at all, in which
// case the whole document becomes one section titled fallbackTitle. An empty
// document produces no sections.
func ChunkMarkdown(text, fallbackTitle string) []Section {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")

	var sections []Section
	var curTitle string
	var curLines []string
	inSection := false

	flush := func() {
		if !inSection {
			return
		}
		content := strings.TrimSpace(strings.Join(curLines, "\n"))
		sections = append(sections, Section{Title: curTitle, Content: content})
	}

	for _, line := range lines {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			curTitle = m[1]
			curLines = []string{line}
			inSection = true
			continue
		}
		if inSection {
			curLines = append(curLines, line)
		}
	}
	flush()

	if sections == nil {
		return []Section{{Title: fallbackTitle, Content: strings.TrimSpace(text)}}
	}
	return sections
}
