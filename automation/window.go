package automation

import "time"

// The external send window, in tenant-local hours: [08:00, 18:00). Both ends
// are far from the 02:00 DST transitions, so a window edge always exists.
const (
	sendWindowStartHour = 8
	sendWindowEndHour   = 18
)

// InSendWindow reports whether now falls in [08:00, 18:00) in loc.
func InSendWindow(now time.Time, loc *time.Location) bool {
	h := now.In(loc).Hour()
	return h >= sendWindowStartHour && h < sendWindowEndHour
}

// NextSendWindow returns now when inside the window, otherwise the next 08:00
// instant in loc.
func NextSendWindow(now time.Time, loc *time.Location) time.Time {
	if InSendWindow(now, loc) {
		return now
	}
	l := now.In(loc)
	start := time.Date(l.Year(), l.Month(), l.Day(), sendWindowStartHour, 0, 0, 0, loc)
	if !l.Before(start) {
		start = time.Date(l.Year(), l.Month(), l.Day()+1, sendWindowStartHour, 0, 0, 0, loc)
	}
	return start
}
