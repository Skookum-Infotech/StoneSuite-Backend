package installschedule

import (
	"errors"
	"fmt"
	"time"

	"stonesuite-backend/dateonly"
)

// Wall-clock layouts accepted from clients.
const (
	layoutLocal = "2006-01-02T15:04"
	layoutDate  = dateonly.Layout

	maxDurationDays = int(MaxDuration / (24 * time.Hour))

	fieldStart = "startLocal"
	fieldEnd   = "endLocal"
)

// ParseLocal converts a "2006-01-02T15:04" wall clock in loc to an instant, rejecting DST-gap times.
func ParseLocal(s string, loc *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation(layoutLocal, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse local time %q: %w", s, err)
	}
	// A wall clock that does not round-trip through loc fell in a DST gap.
	if parsed.Format(layoutLocal) != s {
		return time.Time{}, fmt.Errorf("%q in %s: %w", s, loc, ErrNonexistentLocalTime)
	}
	// An ambiguous fall-back hour occurs twice and Go may return either instant.
	// Always pick the first occurrence: if an hour earlier shows the same wall
	// clock, that earlier instant is the first one.
	if earlier := parsed.Add(-time.Hour); earlier.Format(layoutLocal) == s {
		return earlier, nil
	}
	return parsed, nil
}

// ResolveInterval turns client wall-clock bounds into a UTC interval in loc.
func ResolveInterval(startLocal, endLocal string, allDay bool, loc *time.Location) (Interval, error) {
	if allDay {
		return resolveAllDay(startLocal, endLocal, loc)
	}
	start, err := parseSlot(fieldStart, startLocal, loc)
	if err != nil {
		return Interval{}, err
	}
	end, err := parseSlot(fieldEnd, endLocal, loc)
	if err != nil {
		return Interval{}, err
	}
	if !end.After(start) {
		return Interval{}, &ValidationError{Field: fieldEnd, Message: "must be after start"}
	}
	if end.Sub(start) > MaxDuration {
		return Interval{}, &ValidationError{Field: fieldEnd, Message: fmt.Sprintf("duration must be at most %d days", maxDurationDays)}
	}
	return Interval{
		Start: start.UTC(), End: end.UTC(),
		LocalStartDate: dateonly.New(start), LocalEndDate: dateonly.New(end),
		TZ: loc.String(),
	}, nil
}

func parseSlot(field, s string, loc *time.Location) (time.Time, error) {
	t, err := ParseLocal(s, loc)
	if errors.Is(err, ErrNonexistentLocalTime) {
		return time.Time{}, fmt.Errorf("%s: %w", field, err)
	}
	if err != nil {
		return time.Time{}, &ValidationError{Field: field, Message: err.Error()}
	}
	if t.Minute()%SlotMinutes != 0 {
		return time.Time{}, &ValidationError{Field: field, Message: fmt.Sprintf("minutes must be in %d-minute steps", SlotMinutes)}
	}
	return t, nil
}

func resolveAllDay(startLocal, endLocal string, loc *time.Location) (Interval, error) {
	sd, err := dateonly.Parse(startLocal)
	if err != nil {
		return Interval{}, &ValidationError{Field: fieldStart, Message: "must be YYYY-MM-DD"}
	}
	ed, err := dateonly.Parse(endLocal)
	if err != nil {
		return Interval{}, &ValidationError{Field: fieldEnd, Message: "must be YYYY-MM-DD"}
	}
	if ed.Before(sd.Time) {
		return Interval{}, &ValidationError{Field: fieldEnd, Message: "must not be before start"}
	}
	sy, sm, sdd := sd.Date()
	ey, em, edd := ed.Date()
	start := time.Date(sy, sm, sdd, 0, 0, 0, 0, loc)
	end := time.Date(ey, em, edd, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	// The extra hour absorbs a 25-hour DST day inside the span.
	if end.Sub(start) > MaxDuration+time.Hour {
		return Interval{}, &ValidationError{Field: fieldEnd, Message: fmt.Sprintf("duration must be at most %d days", maxDurationDays)}
	}
	return Interval{
		Start: start.UTC(), End: end.UTC(), AllDay: true,
		LocalStartDate: sd, LocalEndDate: ed, TZ: loc.String(),
	}, nil
}
