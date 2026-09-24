package services

import (
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/config"
)

// portalPath is the customer portal's root route on the frontend origin (see
// controllers.customerInviteLink, which lives under it).
const portalPath = "/portal"

// emailDateLayout is how dates appear in email detail rows, e.g. "Sep 16, 2026".
const emailDateLayout = "Jan 2, 2006"

// bytesPerKB is the divisor for human-readable attachment sizes.
const bytesPerKB = 1024

// appURL turns an app-relative route ("/sales/invoice/<id>") into an absolute
// link on the frontend origin. A relative href is dead in an inbox, so an
// empty path or an unset FrontendURL yields "" (no link) rather than a broken one.
func appURL(path string) string {
	base := strings.TrimRight(config.AppConfig.FrontendURL, "/")
	if base == "" || path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// AppURL is the exported form of appURL for callers outside this package.
func AppURL(path string) string { return appURL(path) }

// PortalURL is the absolute link to the customer portal ("" when FrontendURL
// is unset).
func PortalURL() string { return appURL(portalPath) }

// FormatEmailDate renders t as an email detail-row date ("Sep 16, 2026").
func FormatEmailDate(t time.Time) string { return t.Format(emailDateLayout) }

// FormatEmailMoney renders an amount with a currency symbol and thousands
// separators ("$12,450.00"); an empty symbol defaults to "$".
func FormatEmailMoney(symbol string, amount float64) string {
	if symbol == "" {
		symbol = "$"
	}
	sign := ""
	if amount < 0 {
		sign, amount = "-", -amount
	}
	whole := fmt.Sprintf("%.2f", amount)
	if whole == "0.00" {
		sign = ""
	}
	intPart, frac := whole[:len(whole)-3], whole[len(whole)-3:]
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + symbol + b.String() + frac
}

// FormatFileSize renders a byte count for the attachment card ("245 KB").
func FormatFileSize(n int) string {
	switch {
	case n < bytesPerKB:
		return fmt.Sprintf("%d B", n)
	case n < bytesPerKB*bytesPerKB:
		return fmt.Sprintf("%d KB", (n+bytesPerKB/2)/bytesPerKB)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(bytesPerKB*bytesPerKB))
	}
}
