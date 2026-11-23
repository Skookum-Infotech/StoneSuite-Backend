package guard

import (
	"regexp"
	"sort"
)

// injectionPhrases are lowercase substrings characteristic of an attempt to
// steer a model reading this text as instructions rather than as data —
// the indirect-prompt-injection surface described in the architecture plan:
// once uploaded documents or conversation history reach the model's
// context, anyone who can write that text can attempt this.
//
// A fixed phrase list is necessarily incomplete: it catches known, common
// phrasings, not every way to phrase an injection attempt, and is meant to
// be cheap and explainable, not exhaustive — see the package doc.
var injectionPhrases = []string{
	"ignore previous instructions",
	"ignore all previous instructions",
	"ignore the above instructions",
	"disregard previous instructions",
	"disregard the above",
	"disregard all prior",
	"forget your previous instructions",
	"forget everything above",
	"you are now",
	"act as if you were",
	"pretend you are",
	"new instructions:",
	"system prompt:",
	"reveal your instructions",
	"reveal your system prompt",
	"do anything now",
	"override your instructions",
	"this is a system message",
}

// injectionRes are injectionPhrases compiled case-insensitively. Matching the
// original text directly (rather than strings.ToLower of it) keeps offsets
// valid: lowercasing can change a character's byte length (e.g. "İ" or the
// Kelvin sign), which shifted every later offset and could slice past the end
// of text.
var injectionRes = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(injectionPhrases))
	for i, p := range injectionPhrases {
		out[i] = regexp.MustCompile("(?i)" + regexp.QuoteMeta(p))
	}
	return out
}()

// ScanInjection finds injectionPhrases occurrences in text, case-insensitive.
// Findings are returned in text order (sorted by Start).
func ScanInjection(text string) []Finding {
	var findings []Finding
	for _, re := range injectionRes {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			findings = append(findings, Finding{
				Kind: KindPromptInjection, Start: loc[0], End: loc[1], Excerpt: text[loc[0]:loc[1]],
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Start < findings[j].Start })
	return findings
}
