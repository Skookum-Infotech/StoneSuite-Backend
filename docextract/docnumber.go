package docextract

import (
	"regexp"
	"strings"
)

// docNumberPrefixRe strips a leading label such as "INV-", "PO #", "No." or "#".
var docNumberPrefixRe = regexp.MustCompile(`^(?:(?:invoice|inv|purchase\s*order|po|p\.o\.|order|number|num|no|ref)\b\.?\s*)?[#:.\s-]*`)

// docNumberGluedLabelRe matches a label glued straight onto digits ("po7",
// "INV0042"), which docNumberPrefixRe's word boundary cannot see. Group 1 is the
// number. A word that merely starts with a label ("porter-12") does not match.
var docNumberGluedLabelRe = regexp.MustCompile(`^(?:invoice|inv|po|order|num|no|ref)\.?([0-9].*)$`)

// docNumberSepRe matches every non-alphanumeric rune.
var docNumberSepRe = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeDocNumber reduces a document number to a comparison key: lower
// case, a leading INV-/PO-/No./# label removed, separators dropped and leading
// zeros trimmed, so "INV-000123", "#123" and "123" compare equal. An empty or
// separator-only input returns "".
func NormalizeDocNumber(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	if m := docNumberGluedLabelRe.FindStringSubmatch(t); m != nil {
		t = m[1]
	}
	t = docNumberPrefixRe.ReplaceAllString(t, "")
	t = docNumberSepRe.ReplaceAllString(t, "")
	if t == "" {
		return ""
	}
	trimmed := strings.TrimLeft(t, "0")
	if trimmed == "" {
		return "0"
	}
	return trimmed
}
