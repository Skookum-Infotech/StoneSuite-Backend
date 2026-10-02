package docextract

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Issues reported by the strict number parsers.
const (
	IssueInvalid   = "invalid"
	IssueEUDecimal = "eu_decimal"
	IssuePrecision = "precision"
)

const (
	maxNumberDigits = 15
	centsPerUnit    = 100
	milliPerUnit    = 1000
	twoDigitPivot   = 70 // two-digit years below this are 20xx
	century         = 100
	baseTen         = 10
)

var (
	usMoneyRe  = regexp.MustCompile(`^(\d{1,3}(,\d{3})+|\d+)?(\.\d+)?$`)
	euMoneyRe  = regexp.MustCompile(`^(\d{1,3}(\.\d{3})+,\d{1,2}|\d+,\d{1,2})$`)
	currencyRe = regexp.MustCompile(`(?i)USD|[$€£¥]`)
)

// NumberFlagsUsable reports whether a parse issue still leaves a usable value.
func NumberFlagsUsable(issue string) bool { return issue == "" || issue == IssuePrecision }

// ParseMoney strictly parses a currency string into cents. It never guesses:
// letters inside digits are invalid and European 1.234,56 is reported as
// IssueEUDecimal with a zero value. More than two decimals round half up and
// report IssuePrecision.
func ParseMoney(s string) (Cents, string) {
	t := strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		neg = true
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "CR")
	if strings.HasPrefix(t, "-") {
		neg = true
		t = strings.TrimSpace(t[1:])
	}
	if strings.HasSuffix(t, "-") {
		neg = true
		t = strings.TrimSpace(t[:len(t)-1])
	}
	t = strings.TrimSpace(currencyRe.ReplaceAllString(t, ""))
	if strings.HasPrefix(t, "-") {
		neg = true
		t = strings.TrimSpace(t[1:])
	}
	if t == "" || len(t) > maxNumberDigits+4 {
		return 0, IssueInvalid
	}
	if euMoneyRe.MatchString(t) {
		return 0, IssueEUDecimal
	}
	if !usMoneyRe.MatchString(t) || t == "." {
		return 0, IssueInvalid
	}
	t = strings.ReplaceAll(t, ",", "")
	whole, frac, _ := strings.Cut(t, ".")
	issue := ""
	if len(frac) > 2 {
		issue = IssuePrecision
	}
	w, err := parseDigits(whole)
	if err != nil {
		return 0, IssueInvalid
	}
	cents := w * centsPerUnit
	padded := (frac + "00")[:2]
	f, _ := parseDigits(padded)
	cents += f
	if len(frac) > 2 && frac[2] >= '5' {
		cents++
	}
	if neg {
		cents = -cents
	}
	return Cents(cents), issue
}

// parseDigits parses a digit-only string, treating empty as zero.
func parseDigits(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, baseTen, 64)
	if err != nil {
		return 0, fmt.Errorf("parse digits %q: %w", s, err)
	}
	return v, nil
}

// FormatCents renders cents as a plain decimal string ("1234.56").
func FormatCents(c Cents) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, int64(c)/centsPerUnit, int64(c)%centsPerUnit)
}

var qtyRe = regexp.MustCompile(`^(\d{1,3}(,\d{3})+|\d+)?(\.\d+)?$`)

// ParseQty strictly parses a quantity into milli-units (12.5 -> 12500). More
// than three decimals round half up with IssuePrecision.
func ParseQty(s string) (int64, string) {
	t := strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(t, "-") {
		neg = true
		t = strings.TrimSpace(t[1:])
	}
	if t == "" || t == "." || len(t) > maxNumberDigits+4 {
		return 0, IssueInvalid
	}
	if euMoneyRe.MatchString(t) {
		return 0, IssueEUDecimal
	}
	if !qtyRe.MatchString(t) {
		return 0, IssueInvalid
	}
	t = strings.ReplaceAll(t, ",", "")
	whole, frac, _ := strings.Cut(t, ".")
	issue := ""
	if len(frac) > 3 {
		issue = IssuePrecision
	}
	w, err := parseDigits(whole)
	if err != nil {
		return 0, IssueInvalid
	}
	f, _ := parseDigits((frac + "000")[:3])
	v := w*milliPerUnit + f
	if len(frac) > 3 && frac[3] >= '5' {
		v++
	}
	if neg {
		v = -v
	}
	return v, issue
}

// FormatMilli renders milli-units as a plain decimal ("12.5", "12").
func FormatMilli(m int64) string {
	sign := ""
	if m < 0 {
		sign = "-"
		m = -m
	}
	s := fmt.Sprintf("%d.%03d", m/milliPerUnit, m%milliPerUnit)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return sign + s
}

var (
	dateSlashRe = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{2}|\d{4})$`)
	dateISORe   = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	dateWordRe  = regexp.MustCompile(`^([A-Za-z]{3,9})\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(\d{4})$`)
	months      = map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
		"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
		"sep": time.September, "sept": time.September, "oct": time.October,
		"nov": time.November, "dec": time.December,
	}
)

// DateLayoutISO is the normalized date format.
const DateLayoutISO = "2006-01-02"

// ParseDate parses a US-format date (MM/DD/YYYY, M/D/YY, "Jan 2, 2026",
// 2026-01-02) and returns it as ISO. Impossible dates (Feb 30) are rejected.
func ParseDate(s string) (string, bool) {
	t := strings.TrimSpace(s)
	var y, m, d int
	switch {
	case dateSlashRe.MatchString(t):
		g := dateSlashRe.FindStringSubmatch(t)
		m, _ = strconv.Atoi(g[1])
		d, _ = strconv.Atoi(g[2])
		y, _ = strconv.Atoi(g[3])
		if len(g[3]) == 2 {
			if y < twoDigitPivot {
				y += 2000
			} else {
				y += 2000 - century
			}
		}
	case dateISORe.MatchString(t):
		g := dateISORe.FindStringSubmatch(t)
		y, _ = strconv.Atoi(g[1])
		m, _ = strconv.Atoi(g[2])
		d, _ = strconv.Atoi(g[3])
	case dateWordRe.MatchString(t):
		g := dateWordRe.FindStringSubmatch(t)
		mon, ok := months[strings.ToLower(g[1])]
		if !ok {
			mon, ok = months[strings.ToLower(g[1])[:3]]
			if !ok {
				return "", false
			}
		}
		m = int(mon)
		d, _ = strconv.Atoi(g[2])
		y, _ = strconv.Atoi(g[3])
	default:
		return "", false
	}
	tm := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if tm.Year() != y || int(tm.Month()) != m || tm.Day() != d || m < 1 || m > 12 {
		return "", false
	}
	return tm.Format(DateLayoutISO), true
}
