package automation

import (
	"log/slog"
	"strings"
	"time"
)

// TenantLocation resolves a tenant's IANA timezone name, falling back to
// fallback (or UTC when nil) for a blank or unrecognised name.
func TenantLocation(tzName string, fallback *time.Location) *time.Location {
	loc := fallback
	if loc == nil {
		loc = time.UTC
	}
	if name := strings.TrimSpace(tzName); name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
		slog.Warn("automation: invalid tenant timezone, using fallback", "timezone", name, "fallback", loc.String())
	}
	return loc
}

// TenantToday returns the calendar date (midnight, in the resolved location)
// that it currently is for a tenant whose company profile names tzName. An
// empty or unrecognised IANA name falls back to fallback, so a tenant that
// never filled in its timezone still gets a sensible US-local date rather than
// the server's UTC one. A nil fallback is treated as UTC.
func TenantToday(now time.Time, tzName string, fallback *time.Location) time.Time {
	loc := TenantLocation(tzName, fallback)
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
