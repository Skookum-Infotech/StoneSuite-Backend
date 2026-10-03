// Package dateonly holds a calendar date read from a Postgres DATE column.
//
// A DATE has no time or zone, but scanned into time.Time it marshals as
// "2026-01-02T00:00:00Z" — an instant at UTC midnight, which every browser west
// of UTC renders as the previous day. Date marshals as "2026-01-02" instead,
// the shape the frontend's date helpers treat as a calendar day. It embeds
// time.Time so date arithmetic and comparisons keep working unchanged.
package dateonly

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Layout is the JSON and text form of a Date.
const Layout = "2006-01-02"

// Date is a calendar day, always held as midnight UTC.
type Date struct {
	time.Time
}

// New returns the calendar day t falls on (in t's own location) as a Date.
func New(t time.Time) Date {
	y, m, d := t.Date()
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// Parse reads a "yyyy-mm-dd" string.
func Parse(s string) (Date, error) {
	t, err := time.Parse(Layout, s)
	if err != nil {
		return Date{}, fmt.Errorf("dateonly: parse %q: %w", s, err)
	}
	return Date{t}, nil
}

// String formats the date as "yyyy-mm-dd".
func (d Date) String() string { return d.Format(Layout) }

// MarshalJSON writes the date as "yyyy-mm-dd".
func (d Date) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Format(Layout))
}

// UnmarshalJSON accepts "yyyy-mm-dd" and, for older clients, RFC 3339.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("dateonly: unmarshal: %w", err)
	}
	if parsed, err := Parse(s); err == nil {
		*d = parsed
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("dateonly: unmarshal %q: %w", s, err)
	}
	*d = New(t)
	return nil
}

// ScanDate lets pgx scan a DATE column straight into a Date.
func (d *Date) ScanDate(v pgtype.Date) error {
	if !v.Valid {
		*d = Date{}
		return nil
	}
	if v.InfinityModifier != pgtype.Finite {
		return fmt.Errorf("dateonly: infinite date not supported")
	}
	*d = New(v.Time)
	return nil
}

// DateValue lets pgx write a Date to a DATE parameter.
func (d Date) DateValue() (pgtype.Date, error) {
	return pgtype.Date{Time: d.Time, Valid: true}, nil
}

// Scan implements sql.Scanner for drivers and column types other than DATE.
func (d *Date) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*d = Date{}
	case time.Time:
		*d = New(v)
	case string:
		parsed, err := Parse(v)
		if err != nil {
			return err
		}
		*d = parsed
	default:
		return fmt.Errorf("dateonly: cannot scan %T", src)
	}
	return nil
}

// Value implements driver.Valuer.
func (d Date) Value() (driver.Value, error) { return d.Format(Layout), nil }
