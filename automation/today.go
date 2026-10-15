package automation

import (
	"log/slog"
	"strings"
	"time"
)

// TenantToday returns the calendar date (midnight, in the resolved location)
// that it currently is for a tenant whose company profile names tzName. An
// empty or unrecognised IANA name falls back to fallback, so a tenant that
// never filled in its timezone still gets a sensible US-local date rather than
// the server's UTC one. A nil fallback is treated as UTC.
func TenantToday(now time.Time, tzName string, fallback *time.Location) time.Time {
	loc := fallback
	if loc == nil {
		loc = time.UTC
	}
	if name := strings.TrimSpace(tzName); name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			loc = l
		} else {
			slog.Warn("automation: invalid tenant timezone, using fallback", "timezone", name, "fallback", loc.String())
		}
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
