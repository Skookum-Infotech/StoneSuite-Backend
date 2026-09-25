package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
)

func TestAppURL(t *testing.T) {
	tests := []struct {
		name     string
		frontend string
		path     string
		want     string
	}{
		{"joins base and path", "https://app.example.com", "/sales/invoice/1", "https://app.example.com/sales/invoice/1"},
		{"trims trailing slash on base", "https://app.example.com/", "/sales/invoice/1", "https://app.example.com/sales/invoice/1"},
		{"adds missing leading slash", "https://app.example.com", "sales/invoice/1", "https://app.example.com/sales/invoice/1"},
		{"empty path has no link", "https://app.example.com", "", ""},
		{"unset frontend has no link (a relative href is dead in an inbox)", "", "/sales/invoice/1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withTestEmailBrand(t)
			config.AppConfig.FrontendURL = tt.frontend

			assert.Equal(t, tt.want, appURL(tt.path))
		})
	}
}

func TestPortalURL(t *testing.T) {
	withTestEmailBrand(t)
	assert.Equal(t, "https://app.stonesuite.io/portal", PortalURL())
	config.AppConfig.FrontendURL = ""
	assert.Empty(t, PortalURL(), "no dead relative link when the frontend is unset")
}

func TestFormatEmailMoney(t *testing.T) {
	tests := []struct {
		name   string
		symbol string
		amount float64
		want   string
	}{
		{"thousands separators", "$", 12450, "$12,450.00"},
		{"default symbol", "", 5, "$5.00"},
		{"millions", "$", 1234567.891, "$1,234,567.89"},
		{"other symbol", "₹", 999.5, "₹999.50"},
		{"negative", "$", -1500, "-$1,500.00"},
		{"tiny negative is not negative zero", "$", -0.001, "$0.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatEmailMoney(tt.symbol, tt.amount))
		})
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want string
	}{
		{"bytes", 512, "512 B"},
		{"kilobytes round", 245 * 1024, "245 KB"},
		{"megabytes", 3 * 1024 * 1024, "3.0 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatFileSize(tt.n))
		})
	}
}

func TestFormatEmailDate(t *testing.T) {
	assert.Equal(t, "Sep 16, 2026", FormatEmailDate(time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)))
}
