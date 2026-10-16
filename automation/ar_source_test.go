package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestARReason(t *testing.T) {
	hold := "COLL"
	ok := arFacts{statusCode: "SENT", owes: true, hasSend: true}
	with := func(mut func(*arFacts)) arFacts { f := ok; mut(&f); return f }
	tests := []struct {
		name   string
		f      arFacts
		anchor bool
		email  string
		want   string
	}{
		{"eligible", ok, false, "a@b.com", ""},
		{"deleted", with(func(f *arFacts) { f.deleted = true }), false, "a@b.com", reasonVoided},
		{"customer deleted", with(func(f *arFacts) { f.customerDeleted = true }), false, "a@b.com", reasonVoided},
		{"void with balance", with(func(f *arFacts) { f.statusCode = "VOID" }), false, "a@b.com", reasonVoided},
		{"paid status", with(func(f *arFacts) { f.statusCode = "PAID" }), false, "a@b.com", reasonPaid},
		{"zero balance", with(func(f *arFacts) { f.owes = false }), false, "a@b.com", reasonPaid},
		{"dnc", with(func(f *arFacts) { f.doNotContact = true }), false, "a@b.com", reasonDoNotContact},
		{"ar hold", with(func(f *arFacts) { f.arStatus = &hold }), false, "a@b.com", reasonARHold},
		{"not sent", with(func(f *arFacts) { f.hasSend = false }), false, "a@b.com", reasonNotSent},
		{"anchor changed", ok, true, "a@b.com", reasonAnchorChanged},
		{"no email", ok, false, "", reasonNoEmail},
		{"credit covers", with(func(f *arFacts) { f.creditCovers = true }), false, "a@b.com", reasonCreditCovers},
		{"precedence: void beats paid and dnc", with(func(f *arFacts) { f.statusCode = "VOID"; f.owes = false; f.doNotContact = true }), true, "", reasonVoided},
		{"precedence: dnc beats not sent", with(func(f *arFacts) { f.doNotContact = true; f.hasSend = false }), false, "", reasonDoNotContact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, arReason(tt.f, tt.anchor, tt.email))
		})
	}
}
