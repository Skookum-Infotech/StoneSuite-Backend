package installschedule

import (
	"fmt"
	"time"

	"stonesuite-backend/dateonly"
)

// Wall-clock layouts accepted from clients.
const (
	layoutLocal = "2006-01-02T15:04"
	layoutDate  = dateonly.Layout

	fieldStart = "startLocal"
	fieldEnd   = "endLocal"
)

// ParseLocal converts a "2006-01-02T15:04" wall clock in loc to an instant, rejecting DST-gap times.
func ParseLocal(s string, loc *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation(layoutLocal, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse local time %q: %w", s, err)
	}
	wall, err := time.Parse(layoutLocal, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse wall clock %q: %w", s, err)
	}
	if parsed.Hour() != wall.Hour() || parsed.Minute() != wall.Minute() {
		return time.Time{}, fmt.Errorf("%q in %s: %w", s, loc, ErrNonexistentLocalTime)
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
		return Interval{}, &ValidationError{Field: fieldEnd, Message: "duration must be at most 14 days"}
	}
	return Interval{
		Start: start.UTC(), End: end.UTC(),
		LocalStartDate: dateonly.New(start), LocalEndDate: dateonly.New(end),
		TZ: loc.String(),
	}, nil
}

func parseSlot(field, s string, loc *time.Location) (time.Time, error) {
	t, err := ParseLocal(s, loc)
	if err != nil {
		return time.Time{}, &ValidationError{Field: field, Message: err.Error()}
	}
	if t.Minute()%SlotMinutes != 0 {
		return time.Time{}, &ValidationError{Field: field, Message: "minutes must be in 15-minute steps"}
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
	if end.Sub(start) > MaxDuration+time.Hour {
		return Interval{}, &ValidationError{Field: fieldEnd, Message: "duration must be at most 14 days"}
	}
	return Interval{
		Start: start.UTC(), End: end.UTC(), AllDay: true,
		LocalStartDate: sd, LocalEndDate: ed, TZ: loc.String(),
	}, nil
}
