package journal

import "time"

// isClosed reports whether effectiveDate falls on or before closedThrough,
// comparing calendar dates only (the time-of-day component is ignored, since
// closedThrough is always midnight and effectiveDate may not be — e.g. a
// reversal's effective date is time.Now()).
func isClosed(effectiveDate, closedThrough time.Time) bool {
	ey, em, ed := effectiveDate.Date()
	cy, cm, cd := closedThrough.Date()
	e := time.Date(ey, em, ed, 0, 0, 0, 0, time.UTC)
	c := time.Date(cy, cm, cd, 0, 0, 0, 0, time.UTC)
	return !e.After(c)
}
