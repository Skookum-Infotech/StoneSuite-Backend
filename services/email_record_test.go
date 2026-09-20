package services

import (
	"testing"

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

func TestBuildRecordEmailHTML(t *testing.T) {
	full := RecordEmail{
		Badge:   "Approval Needed",
		Subject: "Invoice INV-001",
		Verb:    "needs your approval",
		Message: "Submitted for approval.",
		Path:    "/sales/invoice/abc-123",
		CTA:     "Review invoice",
	}

	tests := []struct {
		name        string
		mutate      func(*RecordEmail)
		contains    []string
		notContains []string
	}{
		{
			name: "renders through the shared reference shell",
			contains: []string{
				"<!DOCTYPE html>",
				referenceBannerGradient,
				">STONE SUITE<",
				">APPROVAL NEEDED<",
				"Invoice INV-001<br>",
				`<span style="color:#c2f589;">needs your approval.</span>`,
				"Submitted for approval.",
				"Review invoice",
				`href="https://app.stonesuite.io/sales/invoice/abc-123"`,
			},
			notContains: []string{".io//sales", "📄"},
		},
		{
			name: "free text is HTML-escaped",
			mutate: func(e *RecordEmail) {
				e.Subject = `<script>alert(1)</script>`
				e.Message = `Tom & Jerry <b>x</b>`
				e.CTA = `<i>Go</i>`
				e.Attachment = `<u>.pdf`
			},
			contains:    []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "Tom &amp; Jerry &lt;b&gt;x&lt;/b&gt;", "&lt;i&gt;Go&lt;/i&gt;", "&lt;u&gt;.pdf"},
			notContains: []string{"<script>", "<b>x</b>", "<i>Go</i>", "<u>.pdf"},
		},
		{
			name:        "no route means no button and no dead link",
			mutate:      func(e *RecordEmail) { e.Path = "" },
			contains:    []string{"Submitted for approval."},
			notContains: []string{"Review invoice", "Use this link instead"},
		},
		{
			name:     "attachment renders a file chip",
			mutate:   func(e *RecordEmail) { e.Attachment = "INV-001.pdf" },
			contains: []string{"📄 INV-001.pdf"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withTestEmailBrand(t)
			e := full
			if tt.mutate != nil {
				tt.mutate(&e)
			}

			out := BuildRecordEmailHTML(e)

			for _, want := range tt.contains {
				assert.Contains(t, out, want)
			}
			for _, unwanted := range tt.notContains {
				assert.NotContains(t, out, unwanted)
			}
		})
	}
}
