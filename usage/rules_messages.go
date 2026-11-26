package usage

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	bytesPerGB       = 1 << 30
	gbWholeFrom      = 100.0
	decimalScale     = 10
	thousandsGroup   = 3
	thousandsSep     = ","
	hoursFormat      = "%.1f h"
	gbWholeFormat    = "%.0f GB"
	gbDecimalFormat  = "%.1f GB"
	percentOneFormat = "%.1f%%"
)

// formatBytesGB renders a byte count in GB (1024^3): integer from 100 GB up, one decimal below.
func formatBytesGB(b float64) string {
	gb := b / bytesPerGB
	if math.Round(gb*decimalScale)/decimalScale >= gbWholeFrom {
		return fmt.Sprintf(gbWholeFormat, gb)
	}
	return fmt.Sprintf(gbDecimalFormat, gb)
}

// formatHours renders compute hours with one decimal.
func formatHours(h float64) string {
	return fmt.Sprintf(hoursFormat, h)
}

// formatCount renders a whole number with thousands separators.
func formatCount(v int64) string {
	neg := v < 0
	s := strconv.FormatInt(v, 10)
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > thousandsGroup {
		parts = append([]string{s[len(s)-thousandsGroup:]}, parts...)
		s = s[:len(s)-thousandsGroup]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, thousandsSep)
	if neg {
		return "-" + out
	}
	return out
}

// formatPercentOne renders a percentage value with one decimal.
func formatPercentOne(p float64) string {
	return fmt.Sprintf(percentOneFormat, p)
}

// formatCardValue renders a value in the unit of its card key.
func formatCardValue(cardKey string, v float64) string {
	switch cardKey {
	case CardDB, CardR2:
		return formatBytesGB(v)
	case CardCompute:
		return formatHours(v)
	default:
		return formatCount(int64(math.Round(v)))
	}
}

// pluralize returns singular when n is 1, else plural.
func pluralize(n int64, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
