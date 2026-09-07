// Package chunk splits long text into embedder-sized pieces.
package chunk

import (
	"strings"
	"unicode"
)

// Options controls how Split behaves.
type Options struct {
	// MaxTokens is the approximate token budget per chunk. Required, must be > 0.
	MaxTokens int
	// Overlap is the approximate token count carried from the end of one
	// chunk into the start of the next, so a fact split across a chunk
	// boundary still appears whole in at least one chunk. 0 disables overlap.
	Overlap int
}

// EstimateTokens approximates s's token count via a word-based heuristic:
// English prose averages roughly 0.75 words per token (a token is often a
// sub-word piece), so word count divided by 0.75 — i.e. multiplied by ~1.33 —
// approximates it without needing the target model's actual vocabulary.
// Deliberately conservative (overestimates): Split staying safely under an
// embedder's real context window matters more than hitting it exactly, since
// the embedding server truncates silently past its limit rather than erroring.
func EstimateTokens(s string) int {
	n := len(strings.Fields(s))
	if n == 0 {
		return 0
	}
	return (n*4 + 2) / 3 // n / 0.75, integer rounding
}

// Split breaks text into chunks of at most opts.MaxTokens estimated tokens
// each, overlapping consecutive chunks by opts.Overlap estimated tokens.
// Splits only occur at paragraph or sentence boundaries, so a chunk never
// cuts mid-sentence — this matters for embedding quality (a truncated
// sentence embeds worse than a complete one) and for a small model's
// downstream comprehension of retrieved text.
//
// A single sentence longer than MaxTokens on its own still becomes its own
// (oversized) chunk rather than being cut mid-word — better for an embedder
// to see one long-but-coherent chunk occasionally than for Split to ever
// produce a mid-sentence fragment.
//
// Returns nil for empty or whitespace-only text. Returns a single chunk
// (the whole text, trimmed) when it already fits within MaxTokens.
func Split(text string, opts Options) []string {
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil
	}

	var chunks []string
	var cur []string
	curTokens := 0

	flush := func() {
		if len(cur) == 0 {
			return
		}
		chunks = append(chunks, strings.TrimSpace(strings.Join(cur, " ")))
	}

	for _, s := range sentences {
		st := EstimateTokens(s)
		if curTokens > 0 && curTokens+st > opts.MaxTokens {
			flush()
			cur = overlapTail(cur, opts.Overlap)
			curTokens = EstimateTokens(strings.Join(cur, " "))
		}
		cur = append(cur, s)
		curTokens += st
	}
	flush()

	return chunks
}

// overlapTail returns the trailing sentences of cur whose combined estimated
// token count is closest to (without exceeding) overlapTokens, preserving
// order. Used to seed the next chunk so it isn't a hard cut from the last.
func overlapTail(cur []string, overlapTokens int) []string {
	if overlapTokens <= 0 || len(cur) == 0 {
		return nil
	}
	var tail []string
	tokens := 0
	for i := len(cur) - 1; i >= 0; i-- {
		st := EstimateTokens(cur[i])
		if tokens > 0 && tokens+st > overlapTokens {
			break
		}
		tail = append([]string{cur[i]}, tail...)
		tokens += st
	}
	return tail
}

// splitSentences splits text into paragraphs, then sentences within each
// paragraph on '.', '!', '?' followed by whitespace — a heuristic, not a full
// sentence tokenizer, but sufficient to find safe split points in prose
// (help docs, record field text) without pulling in an NLP dependency.
func splitSentences(text string) []string {
	var out []string
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		out = append(out, splitParagraphSentences(para)...)
	}
	return out
}

func splitParagraphSentences(para string) []string {
	var sentences []string
	start := 0
	runes := []rune(para)
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		// End of a sentence only if followed by whitespace or end of string —
		// avoids splitting mid-number ("3.5"). A spaced abbreviation like
		// "U.S. sales" still splits after "U.S." — this is a heuristic, not a
		// full sentence tokenizer; an oversplit here just means a slightly
		// smaller chunk, never a mid-word cut.
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}
		sentences = append(sentences, strings.TrimSpace(string(runes[start:i+1])))
		start = i + 1
	}
	if start < len(runes) {
		if rest := strings.TrimSpace(string(runes[start:])); rest != "" {
			sentences = append(sentences, rest)
		}
	}
	return sentences
}
