package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMoney(t *testing.T) {
	tests := []struct {
		in    string
		want  Cents
		issue string
	}{
		{"$1,234.56", 123456, ""},
		{"1,234", 123400, ""},
		{"12", 1200, ""},
		{"(12.00)", -1200, ""},
		{"-$5.50", -550, ""},
		{"12.50-", -1250, ""},
		{".75", 75, ""},
		{"$0.05", 5, ""},
		{"12.345", 1235, IssuePrecision},
		{"1,2O0.00", 0, IssueInvalid},
		{"abc", 0, IssueInvalid},
		{"", 0, IssueInvalid},
		{"1.234,56", 0, IssueEUDecimal},
		{"12,50", 0, IssueEUDecimal},
		{"1,23", 0, IssueEUDecimal},
		{"$.", 0, IssueInvalid},
		{"1,2345.00", 0, IssueInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, issue := ParseMoney(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.issue, issue)
		})
	}
}

func TestParseQty(t *testing.T) {
	tests := []struct {
		in    string
		want  int64
		issue string
	}{
		{"12", 12000, ""},
		{"12.5", 12500, ""},
		{"1,200", 1200000, ""},
		{"0.001", 1, ""},
		{"-3", -3000, ""},
		{"1.0005", 1001, IssuePrecision},
		{"1,2O0", 0, IssueInvalid},
		{"1.234,5", 0, IssueEUDecimal},
		{"", 0, IssueInvalid},
		{"x", 0, IssueInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, issue := ParseQty(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.issue, issue)
		})
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"01/02/2026", "2026-01-02", true},
		{"1/2/26", "2026-01-02", true},
		{"12/31/99", "1999-12-31", true},
		{"Jan 2, 2026", "2026-01-02", true},
		{"January 2 2026", "2026-01-02", true},
		{"Sept 9, 2026", "2026-09-09", true},
		{"2026-01-02", "2026-01-02", true},
		{"02/30/2026", "", false},
		{"2026-02-30", "", false},
		{"13/01/2026", "", false},
		{"Foo 2, 2026", "", false},
		{"2/29/2024", "2024-02-29", true},
		{"2/29/2025", "", false},
		{"yesterday", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseDate(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatters(t *testing.T) {
	assert.Equal(t, "1234.56", FormatCents(123456))
	assert.Equal(t, "-0.05", FormatCents(-5))
	assert.Equal(t, "12.5", FormatMilli(12500))
	assert.Equal(t, "12", FormatMilli(12000))
	assert.Equal(t, "-0.001", FormatMilli(-1))
}
