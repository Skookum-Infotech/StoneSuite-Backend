package orderform

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits. The text widths are the columns they are stored in, or the
// sales-order columns they are copied to (a sales order's ship-to address line,
// city, ZIP and e-mail are 100/100/10/100), so an accepted order always fits.
const (
	MaxAreas              = 20
	maxQty                = 20
	maxBacksplashHeightIn = 96
	maxNameLen            = 120
	maxAreaNameLen        = 100
	maxAddressLen         = 100
	maxPhoneLen           = 20
	maxZipLen             = 10
	maxNotesLen           = 1000
	maxInstructionsLen    = 2000

	minPhoneDigits = 7
	maxPhoneDigits = 15

	// installGraceDays matches the Sales Order store's allowance for a caller
	// whose local "today" is still yesterday in UTC.
	installGraceDays = 1

	dateLayout = "2006-01-02"
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// clean drops control characters (a NUL would make Postgres refuse the row) and,
// for single-line fields, collapses runs of whitespace and trims.
func clean(s string, multiline bool) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if multiline {
		return strings.TrimSpace(s)
	}
	return strings.Join(strings.Fields(s), " ")
}

func tooLong(s string, max int) bool { return utf8.RuneCountInString(s) > max }

func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// Normalize validates an order and returns it tidied: text trimmed, values an
// area's kind does not show blanked, Faucet Spread in the row's order. now is
// the caller's clock (UTC) for the install-date check. The first problem found
// is returned as a *ValidationError.
func Normalize(in Input, now time.Time) (Input, error) {
	return normalize(in, now, "")
}

// NormalizeEdit is Normalize for an order that is being changed: an Install Date
// equal to storedInstall is accepted even once it has slipped into the past, the
// way the Sales Order store accepts the date already on an order. Only a
// different date is checked against today.
func NormalizeEdit(in Input, now time.Time, storedInstall string) (Input, error) {
	return normalize(in, now, storedInstall)
}

func normalize(in Input, now time.Time, storedInstall string) (Input, error) {
	out := Input{
		Builder:             clean(in.Builder, false),
		Community:           clean(in.Community, false),
		Plan:                clean(in.Plan, false),
		Phone:               clean(in.Phone, false),
		Email:               clean(in.Email, false),
		InstallAddress:      clean(in.InstallAddress, false),
		City:                clean(in.City, false),
		Zip:                 clean(in.Zip, false),
		MeasureDate:         strings.TrimSpace(in.MeasureDate),
		InstallDate:         strings.TrimSpace(in.InstallDate),
		SpecialInstructions: clean(in.SpecialInstructions, true),
	}
	if err := checkHeader(out, now, storedInstall); err != nil {
		return Input{}, err
	}
	areas, err := normalizeAreas(in.Areas)
	if err != nil {
		return Input{}, err
	}
	out.Areas = areas
	return out, nil
}

type headerRule struct {
	label string
	value string
	max   int
}

func checkHeader(h Input, now time.Time, storedInstall string) error {
	required := []headerRule{
		{"Builder", h.Builder, maxNameLen},
		{"Community", h.Community, maxNameLen},
		{"Phone", h.Phone, maxPhoneLen},
		{"Email", h.Email, maxAddressLen},
		{"Install Address", h.InstallAddress, maxAddressLen},
		{"City", h.City, maxAddressLen},
		{"ZIP", h.Zip, maxZipLen},
	}
	for _, r := range required {
		if r.value == "" {
			return invalid("%s is required.", r.label)
		}
		if tooLong(r.value, r.max) {
			return invalid("%s can be at most %d characters.", r.label, r.max)
		}
	}
	if tooLong(h.Plan, maxNameLen) {
		return invalid("Plan can be at most %d characters.", maxNameLen)
	}
	if tooLong(h.SpecialInstructions, maxInstructionsLen) {
		return invalid("Special Instructions can be at most %d characters.", maxInstructionsLen)
	}
	if d := countDigits(h.Phone); d < minPhoneDigits || d > maxPhoneDigits {
		return invalid("Enter a valid phone number.")
	}
	if !emailPattern.MatchString(h.Email) {
		return invalid("Enter a valid email address.")
	}
	return checkDates(h.MeasureDate, h.InstallDate, now, storedInstall)
}

func checkDates(measureRaw, installRaw string, now time.Time, storedInstall string) error {
	if measureRaw == "" {
		return invalid("Measure Date is required.")
	}
	if installRaw == "" {
		return invalid("Install Date is required.")
	}
	measure, err := time.Parse(dateLayout, measureRaw)
	if err != nil {
		return invalid("Measure Date must be a valid date.")
	}
	install, err := time.Parse(dateLayout, installRaw)
	if err != nil {
		return invalid("Install Date must be a valid date.")
	}
	if install.Before(measure) {
		return invalid("Install Date cannot be before the Measure Date.")
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	unchanged := storedInstall != "" && installRaw == storedInstall
	if !unchanged && install.Before(today.AddDate(0, 0, -installGraceDays)) {
		return invalid("Install Date cannot be in the past.")
	}
	return nil
}
