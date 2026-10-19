package recordemail

import (
	"regexp"
	"strings"
)

// Markers that begin the quoted history below a reply. Mail clients disagree on
// the wording, so each family is matched separately; the earliest hit wins.
var (
	// Gmail / Apple Mail / Thunderbird: "On Tue, Oct 14, 2026 at 3:12 PM X <x@y> wrote:".
	reOnWrote = regexp.MustCompile(`(?i)^on\s.+\swrote:\s*$`)
	// Outlook, Yahoo and others: a divider or a header block opening with From:/Sent:.
	reOriginal = regexp.MustCompile(`(?i)^-{2,}\s*(original message|forwarded message)\s*-{2,}\s*$`)
	reFromLine = regexp.MustCompile(`(?i)^from:\s.+`)
	reSentLine = regexp.MustCompile(`(?i)^(sent|date):\s.+`)
	reRuleLine = regexp.MustCompile(`^_{5,}\s*$`)
)

// headerLookahead is how many lines below a From: line a Sent:/Date: line may sit
// for the pair to count as an Outlook-style header block.
const headerLookahead = 4

// StripQuotedReply returns just the part of a plain-text reply the sender
// actually wrote, dropping the quoted earlier messages mail clients append
// below it ("On ... wrote:", Outlook's From/Sent block, "> " lines). If stripping
// would leave nothing (a reply written entirely between quoted lines) the
// original is returned trimmed, so a message is never shown empty.
func StripQuotedReply(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")

	cut := len(lines)
	for i := range lines {
		if isQuoteStart(lines, i) {
			cut = i
			break
		}
	}

	kept := strings.TrimSpace(strings.Join(lines[:cut], "\n"))
	if kept == "" {
		return strings.TrimSpace(text)
	}
	return kept
}

// isQuoteStart reports whether the quoted history begins at lines[i].
func isQuoteStart(lines []string, i int) bool {
	line := strings.TrimSpace(lines[i])
	switch {
	case strings.HasPrefix(line, ">"):
		return true
	case reOriginal.MatchString(line):
		return true
	case reOnWrote.MatchString(line):
		return true
	case strings.HasPrefix(strings.ToLower(line), "on ") && !strings.HasSuffix(line, ":") && i+1 < len(lines):
		// Gmail wraps a long attribution: "On ..., X <x@y>" then "wrote:".
		return reOnWrote.MatchString(line + " " + strings.TrimSpace(lines[i+1]))
	case reRuleLine.MatchString(line):
		return headerBlockAt(lines, i+1)
	case reFromLine.MatchString(line):
		return headerBlockAt(lines, i)
	}
	return false
}

// headerBlockAt reports whether lines[i] is a From: line with a Sent:/Date: line
// within the next few lines. Requiring both keeps an ordinary sentence that
// happens to start "From: the warehouse" from cutting a real message short.
func headerBlockAt(lines []string, i int) bool {
	if i >= len(lines) || !reFromLine.MatchString(strings.TrimSpace(lines[i])) {
		return false
	}
	for j := i + 1; j < len(lines) && j <= i+headerLookahead; j++ {
		if reSentLine.MatchString(strings.TrimSpace(lines[j])) {
			return true
		}
	}
	return false
}
