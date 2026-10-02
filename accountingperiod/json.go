package accountingperiod

import (
	"encoding/json"
	"fmt"
	"time"
)

// dateLayout is the wire format for calendar-date fields (yyyy-mm-dd).
const dateLayout = "2006-01-02"

// formatDate renders a time as a yyyy-mm-dd calendar date.
func formatDate(t time.Time) string {
	return t.Format(dateLayout)
}

// formatDatePtr renders an optional time as a yyyy-mm-dd date; nil stays nil.
func formatDatePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatDate(*t)
	return &s
}

// MarshalJSON encodes Start and End as yyyy-mm-dd dates.
func (p Period) MarshalJSON() ([]byte, error) {
	type alias Period
	b, err := json.Marshal(struct {
		alias
		Start string `json:"start"`
		End   string `json:"end"`
	}{alias(p), formatDate(p.Start), formatDate(p.End)})
	if err != nil {
		return nil, fmt.Errorf("marshal period: %w", err)
	}
	return b, nil
}

// MarshalJSON encodes Start and End as yyyy-mm-dd dates.
func (q Quarter) MarshalJSON() ([]byte, error) {
	type alias Quarter
	b, err := json.Marshal(struct {
		alias
		Start string `json:"start"`
		End   string `json:"end"`
	}{alias(q), formatDate(q.Start), formatDate(q.End)})
	if err != nil {
		return nil, fmt.Errorf("marshal quarter: %w", err)
	}
	return b, nil
}

// MarshalJSON encodes Start and End as yyyy-mm-dd dates.
func (f FiscalYear) MarshalJSON() ([]byte, error) {
	type alias FiscalYear
	b, err := json.Marshal(struct {
		alias
		Start string `json:"start"`
		End   string `json:"end"`
	}{alias(f), formatDate(f.Start), formatDate(f.End)})
	if err != nil {
		return nil, fmt.Errorf("marshal fiscal year: %w", err)
	}
	return b, nil
}

// MarshalJSON encodes BasePeriodStart and BooksClosedThrough as yyyy-mm-dd dates.
func (c Calendar) MarshalJSON() ([]byte, error) {
	type alias Calendar
	b, err := json.Marshal(struct {
		alias
		BasePeriodStart    *string `json:"basePeriodStart,omitempty"`
		BooksClosedThrough *string `json:"booksClosedThrough,omitempty"`
	}{alias(c), formatDatePtr(c.BasePeriodStart), formatDatePtr(c.BooksClosedThrough)})
	if err != nil {
		return nil, fmt.Errorf("marshal calendar: %w", err)
	}
	return b, nil
}

// MarshalJSON encodes BooksClosedThrough as a yyyy-mm-dd date, or null when unset.
func (r StatusChangeResult) MarshalJSON() ([]byte, error) {
	type alias StatusChangeResult
	b, err := json.Marshal(struct {
		alias
		BooksClosedThrough *string `json:"booksClosedThrough"`
	}{alias(r), formatDatePtr(r.BooksClosedThrough)})
	if err != nil {
		return nil, fmt.Errorf("marshal status change result: %w", err)
	}
	return b, nil
}
