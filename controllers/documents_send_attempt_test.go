package controllers

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestDocumentSendKeys(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) string
	}{
		{"notify key", documentSendNotifyKey},
		{"owner ping key", documentOwnerPingKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := tt.fn("send-1")
			assert.Regexp(t, hex64, a)
			assert.Equal(t, a, tt.fn("send-1"), "deterministic")
			assert.NotEqual(t, a, tt.fn("send-2"), "differs by id")
			assert.Regexp(t, hex64, tt.fn(""))
		})
	}
	assert.NotEqual(t, documentSendNotifyKey("send-1"), documentOwnerPingKey("send-1"))
}

func TestSendFingerprint(t *testing.T) {
	base := func() *preparedSend {
		p := &preparedSend{to: []string{"a@x.com"}, cc: []string{"c@x.com"}, subject: "S", message: "M"}
		p.meta.WorkflowKey = "invoice"
		return p
	}
	tests := []struct {
		name   string
		mutate func(p *preparedSend)
		same   bool
	}{
		{"identical", func(p *preparedSend) {}, true},
		{"workflow", func(p *preparedSend) { p.meta.WorkflowKey = "quote" }, false},
		{"to", func(p *preparedSend) { p.to = []string{"b@x.com"} }, false},
		{"cc", func(p *preparedSend) { p.cc = nil }, false},
		{"subject", func(p *preparedSend) { p.subject = "T" }, false},
		{"message", func(p *preparedSend) { p.message = "N" }, false},
		{"to/cc boundary", func(p *preparedSend) { p.to, p.cc = []string{"a@x.com", "c@x.com"}, nil }, false},
	}
	want := sendFingerprint(base())
	assert.Regexp(t, hex64, want)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base()
			tt.mutate(p)
			got := sendFingerprint(p)
			assert.Regexp(t, hex64, got)
			assert.Equal(t, tt.same, got == want)
		})
	}
}
