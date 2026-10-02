package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeUnit(t *testing.T) {
	tests := []struct {
		in       string
		canon    string
		category string
		ok       bool
	}{
		{"SF", "sqft", catArea, true},
		{"Sq. Ft.", "sqft", catArea, true},
		{"ft2", "sqft", catArea, true},
		{"m²", "sqm", catArea, true},
		{"SQM", "sqm", catArea, true},
		{"LBS", "lb", catMass, true},
		{"Each", "ea", catCount, true},
		{"pcs", "ea", catCount, true},
		{"Inches", "in", catLength, true},
		{"slab", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			c, cat, ok := NormalizeUnit(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.canon, c)
			assert.Equal(t, tt.category, cat)
		})
	}
}

func TestConvert(t *testing.T) {
	tests := []struct {
		name      string
		qty       int64
		price     Cents
		from, to  string
		wantQty   int64
		wantPrice Cents
		wantOK    bool
	}{
		{name: "same unit unchanged", qty: 5000, price: 1000, from: "SF", to: "sq ft", wantQty: 5000, wantPrice: 1000, wantOK: true},
		{name: "sqm to sqft amount preserved", qty: 12000, price: 10764, from: "sqm", to: "sqft", wantQty: 129167, wantPrice: 1000, wantOK: true},
		{name: "ft to in", qty: 2000, price: 1200, from: "ft", to: "in", wantQty: 24000, wantPrice: 100, wantOK: true},
		{name: "lb to kg", qty: 10000, price: 4536, from: "lb", to: "kg", wantQty: 4536, wantPrice: 10000, wantOK: true},
		{name: "each to pcs", qty: 3000, price: 500, from: "each", to: "pcs", wantQty: 3000, wantPrice: 500, wantOK: true},
		{name: "cross category refused", qty: 1000, price: 1000, from: "sqft", to: "lb"},
		{name: "unknown unit refused", qty: 1000, price: 1000, from: "slab", to: "sqft"},
		{name: "large amount drifts so refused", qty: 1000000, price: 10000, from: "sqm", to: "sqft"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, p, ok := Convert(tt.qty, tt.price, tt.from, tt.to)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantQty, q)
				assert.Equal(t, tt.wantPrice, p)
				drift := lineAmount(tt.qty, tt.price) - lineAmount(q, p)
				assert.LessOrEqual(t, int64(drift)*int64(drift), int64(1))
			}
		})
	}
}
