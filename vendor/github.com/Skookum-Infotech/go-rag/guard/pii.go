package guard

import (
	"regexp"
	"sort"
	"strings"
)

// redactedMask replaces a PII Finding's matched span in Redact's output.
// Fixed-width and content-blind on purpose: a partial mask (e.g. showing the
// last 4 digits of a card) is a policy decision, which this package stays
// out of — see the package doc.
const redactedMask = "[REDACTED]"

// candidateCardRe matches runs of 13-19 digits, optionally grouped with a
// single space or hyphen every few digits (the common human-formatted
// shape: "4111 1111 1111 1111" or "4111-1111-1111-1111") — a superset of
// real card numbers, narrowed down to actual candidates by the Luhn check
// in ScanPII. Matching the loose shape first and validating after keeps this
// one pattern rather than one per card network's length/prefix rules.
var candidateCardRe = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)

// ssnRe matches the canonical hyphenated US SSN shape (XXX-XX-XXXX) only —
// deliberately not the unformatted 9-digit-run form, which would false-positive
// constantly against invoice numbers, phone numbers, and the like. SSNs have
// no checksum analogous to Luhn, so this is a syntactic pattern only and is
// necessarily higher false-positive than the card check; treat a KindSSN
// Finding as a weaker signal than a KindCreditCard one.
var ssnRe = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)

// ScanPII finds PII-shaped substrings in text: Luhn-validated credit card
// numbers and canonically-formatted SSNs. Findings are returned in the
// order their spans start.
func ScanPII(text string) []Finding {
	var findings []Finding

	for _, loc := range candidateCardRe.FindAllStringIndex(text, -1) {
		candidate := text[loc[0]:loc[1]]
		digits := stripNonDigits(candidate)
		if len(digits) < 13 || len(digits) > 19 {
			continue
		}
		if !luhnValid(digits) {
			continue
		}
		findings = append(findings, Finding{Kind: KindCreditCard, Start: loc[0], End: loc[1], Excerpt: candidate})
	}

	for _, loc := range ssnRe.FindAllStringIndex(text, -1) {
		findings = append(findings, Finding{Kind: KindSSN, Start: loc[0], End: loc[1], Excerpt: text[loc[0]:loc[1]]})
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].Start < findings[j].Start })
	return findings
}

// Redact returns text with every ScanPII finding's matched span replaced by
// redactedMask. Overlapping findings (not expected from ScanPII's own
// patterns, but a caller could hand-build one) are resolved by keeping
// whichever starts first and skipping anything that starts before the
// previous replacement ended.
func Redact(text string) string {
	findings := ScanPII(text)
	if len(findings) == 0 {
		return text
	}

	var b strings.Builder
	pos := 0
	for _, f := range findings {
		if f.Start < pos {
			continue // overlaps the previous replacement; skip rather than double-mask
		}
		b.WriteString(text[pos:f.Start])
		b.WriteString(redactedMask)
		pos = f.End
	}
	b.WriteString(text[pos:])
	return b.String()
}

// stripNonDigits removes everything but ASCII digits, for validating a
// loosely-formatted candidate card number.
func stripNonDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// luhnValid reports whether digits (an ASCII digit string) passes the Luhn
// checksum, the standard validity check for card numbers — doubling every
// second digit from the right, subtracting 9 from any result over 9, and
// requiring the total to be a multiple of 10.
func luhnValid(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
