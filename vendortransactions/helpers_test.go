package vendortransactions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/dateonly"
)

func TestOptDate(t *testing.T) {
	d := dateonly.New(time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC))
	tests := []struct {
		name string
		in   *dateonly.Date
		want string
	}{
		{"nil", nil, ""},
		{"set", &d, "2026-10-31"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, optDate(tt.in)) })
	}
}

func TestOptMoney(t *testing.T) {
	zero, neg, frac := 0.0, -12.5, 1234.567
	tests := []struct {
		name string
		in   *float64
		want string
	}{
		{"nil", nil, ""},
		{"zero", &zero, "0.00"},
		{"negative not escaped", &neg, "-12.50"},
		{"rounded", &frac, "1234.57"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, optMoney(tt.in)) })
	}
}

func TestBuildAgingSQL(t *testing.T) {
	today := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		own      bool
		wantArgs int
	}{
		{"all scope", false, 2},
		{"own scope", true, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := buildAgingSQL(granted{Source: vendorBill(), own: tt.own}, 7, 3, today)
			assert.Contains(t, sql, "WHERE is_open GROUP BY 1")
			assert.Contains(t, sql, "days_past_due")
			assert.Equal(t, 7, args[0])
			assert.GreaterOrEqual(t, len(args), 2)
		})
	}
}
