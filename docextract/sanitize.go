package docextract

import (
	"regexp"
	"strings"
	"unicode"
)

// MaskedPlaceholder replaces sensitive payment data found in free text.
const MaskedPlaceholder = "[masked]"

const (
	cardMinDigits    = 13
	cardMaxDigits    = 19
	ibanMinLen       = 15
	ibanMaxLen       = 34
	ibanMod          = 97
	luhnDoubleLimit  = 9
	asciiZero        = '0'
	ibanLetterOffset = 10
)

var (
	// cardCandidateRe finds digit runs (single space/dash between digits) long enough for a card.
	cardCandidateRe = regexp.MustCompile(`\d(?:[ -]?\d){12,}`)
	ibanSpacedRe    = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]){11,30}\b`)
	ibanTokenRe     = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)
	// labelledNumRe captures a 6-17 digit run right after a routing/account label.
	labelledNumRe = regexp.MustCompile(`(?i)\b(?:routing|aba|ach|account|acct)\b(?:\s*(?:no\.?|number|num\.?|#))?\s*[:#]?\s*(\d(?:[ -]?\d){5,16})\b`)
)

// isBidiOrFormat reports runes that can hide or reorder text.
func isBidiOrFormat(r rune) bool {
	return unicode.Is(unicode.Cf, r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// clipRunes cuts s to at most maxRunes runes (no limit when maxRunes <= 0).
func clipRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	return strings.TrimSpace(string(rs[:maxRunes]))
}

// Clean drops control, format and bidi runes, collapses whitespace to single
// spaces, trims and clips to maxRunes runes.
func Clean(s string, maxRunes int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r) || isBidiOrFormat(r):
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return clipRunes(strings.TrimSpace(b.String()), maxRunes)
}

// CleanBlock is Clean for multi-line text: single newlines are kept, runs of
// blank lines and horizontal whitespace collapse.
func CleanBlock(s string, maxRunes int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if c := Clean(ln, 0); c != "" {
			out = append(out, c)
		}
	}
	return clipRunes(strings.Join(out, "\n"), maxRunes)
}

// digitsOnly returns the ASCII digits of s.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// luhnValid reports whether the digit string passes the Luhn check.
func luhnValid(d string) bool {
	sum, double := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - asciiZero)
		if double {
			n *= 2
			if n > luhnDoubleLimit {
				n -= luhnDoubleLimit
			}
		}
		sum += n
		double = !double
	}
	return len(d) > 0 && sum%10 == 0
}

// ibanValid reports whether s (no spaces) passes the IBAN mod-97 check.
func ibanValid(s string) bool {
	if len(s) < ibanMinLen || len(s) > ibanMaxLen {
		return false
	}
	rot := s[4:] + s[:4]
	rem := 0
	for _, r := range rot {
		switch {
		case r >= '0' && r <= '9':
			rem = (rem*10 + int(r-'0')) % ibanMod
		case r >= 'A' && r <= 'Z':
			rem = (rem*100 + int(r-'A') + ibanLetterOffset) % ibanMod
		default:
			return false
		}
	}
	return rem == 1
}

// maskIBAN masks IBAN-shaped tokens that pass mod-97.
func maskIBAN(s string) string {
	return ibanSpacedRe.ReplaceAllStringFunc(s, func(m string) string {
		if ibanValid(strings.ReplaceAll(m, " ", "")) {
			return MaskedPlaceholder
		}
		if tok := ibanTokenRe.FindString(m); tok != "" && ibanValid(tok) {
			return MaskedPlaceholder + m[len(tok):]
		}
		return m
	})
}

// maskLabelled masks digit runs that follow a routing/account label.
func maskLabelled(s string) string {
	idx := labelledNumRe.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		b.WriteString(s[last:m[2]])
		b.WriteString(MaskedPlaceholder)
		last = m[3]
	}
	b.WriteString(s[last:])
	return b.String()
}

// maskCards masks 13-19 digit runs that pass the Luhn check.
func maskCards(s string) string {
	return cardCandidateRe.ReplaceAllStringFunc(s, func(m string) string {
		d := digitsOnly(m)
		if len(d) < cardMinDigits || len(d) > cardMaxDigits || !luhnValid(d) {
			return m
		}
		return MaskedPlaceholder
	})
}

// MaskSensitive replaces card numbers (Luhn), IBANs (mod-97) and labelled
// routing/account numbers in free text with MaskedPlaceholder.
func MaskSensitive(s string) string {
	if s == "" {
		return s
	}
	return maskCards(maskIBAN(maskLabelled(s)))
}
