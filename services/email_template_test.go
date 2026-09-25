package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

// mustEmailHTML renders req's email body through the shared template.
func mustEmailHTML(t *testing.T, req NotificationRequest) string {
	t.Helper()
	out, err := req.EmailHTML()
	require.NoError(t, err)
	return out
}

func mustRender(t *testing.T, e Email) string {
	t.Helper()
	out, err := RenderEmail(e)
	require.NoError(t, err)
	return out
}

// sampleEmail exercises every block of the shared template.
func sampleEmail() Email {
	return Email{
		Preheader: "preheader", Badge: "Approval Needed", Icon: IconDocClock,
		Heading: "Invoice INV-000123", HeadingAccent: "needs your approval.",
		Subtitle: "Please review and take action.",
		Greet:    true, RecipientName: "Alex Approver",
		Paragraphs: []Paragraph{{Bold("Acme Stone Co"), Text(" has sent you invoice "), Link("INV-0042", "https://app.example/portal"), Text(".")}},
		Details:    []Detail{{Label: "Invoice Number", Value: "INV-000123"}, {Label: "Amount", Value: "$12,450.00"}},
		Attachment: &AttachmentCard{FileName: "INV-0042.pdf", Size: "245 KB"},
		ActionURL:  "https://app.example/accept-invite?token=secret", ActionLabel: "Review invoice",
		FinePrint: []string{"This link expires in 1 hour.", "If you didn't request this, ignore it."},
		Reason:    "you're an approver.",
	}
}

func TestRenderEmail_EveryBlockCarriesItsContent(t *testing.T) {
	withTestEmailBrand(t)
	out := mustRender(t, sampleEmail())

	for _, want := range []string{
		"<!DOCTYPE html>",
		">APPROVAL NEEDED<",
		"Invoice <span style=\"white-space:nowrap;\">INV-000123</span><br>",
		`<span style="color:#c2f589;">needs your approval.</span>`,
		">Please review and take action.<",
		">Hello Alex Approver,<",
		`<strong style="color:#18181b;">Acme Stone Co</strong> has sent you invoice <a href="https://app.example/portal"`,
		">INV-0042</a>.",
		">Invoice Number<", ">$12,450.00<",
		"INV-0042.pdf", "245 KB",
		">Review invoice<", "Use this link instead",
		"This link expires in 1 hour.", "If you didn&#39;t request this, ignore it.",
		"You're receiving this because you&#39;re an approver.",
	} {
		assert.Contains(t, out, want)
	}
}

func TestRenderEmail_EscapesEveryField(t *testing.T) {
	withTestEmailBrand(t)
	x := `<script>x</script>`
	out := mustRender(t, Email{
		Preheader: x, Badge: x, Heading: x, HeadingAccent: x, Subtitle: x, Greet: true, RecipientName: x,
		Paragraphs: []Paragraph{{Text(x), Bold(x), Link(x, "https://ok.example")}},
		Details:    []Detail{{Label: x, Value: x}}, Attachment: &AttachmentCard{FileName: x, Size: x},
		ActionURL: "https://ok.example", ActionLabel: x, FinePrint: []string{x}, Reason: x,
	})

	assert.NotContains(t, out, "<script>", "every caller-supplied field is escaped")
	assert.Contains(t, out, "&lt;script&gt;x&lt;/script&gt;")
}

func TestRenderEmail_RejectsUnsafeLinks(t *testing.T) {
	withTestEmailBrand(t)
	e := sampleEmail()
	e.ActionURL = "javascript:alert(1)"
	e.Paragraphs = []Paragraph{{Link("x", "javascript:alert(2)")}}
	out := mustRender(t, e)

	assert.NotContains(t, out, "javascript:", "html/template filters non-http(s) link schemes")
}

func TestRenderEmail_GreetingVariants(t *testing.T) {
	withTestEmailBrand(t)
	tests := []struct {
		name     string
		greet    bool
		who      string
		contains string
		absent   string
	}{
		{"named", true, "Casey", ">Hello Casey,<", ""},
		{"blank name", true, "", ">Hello,<", "Hello ,"},
		{"no greeting", false, "Casey", "", "Hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := sampleEmail()
			e.Greet, e.RecipientName = tt.greet, tt.who
			out := mustRender(t, e)
			if tt.contains != "" {
				assert.Contains(t, out, tt.contains)
			}
			if tt.absent != "" {
				assert.NotContains(t, out, tt.absent)
			}
		})
	}
}

func TestRenderEmail_OptionalBlocksOmittedWhenEmpty(t *testing.T) {
	withTestEmailBrand(t)
	out := mustRender(t, Email{Badge: "Badge", Heading: "Line one", Paragraphs: []Paragraph{{Text("Just a message.")}}})

	assert.Contains(t, out, "Just a message.")
	assert.NotContains(t, out, "Use this link instead", "no link means no CTA button")
	assert.NotContains(t, out, "KB<", "no attachment card")
	assert.NotContains(t, out, `<span style="color:#c2f589;">`, "no heading accent line")
	assert.Contains(t, out, "You're receiving this because you have a StoneSuite account.", "default footer reason")
}

func TestRenderEmail_BannerIconPerType(t *testing.T) {
	withTestEmailBrand(t)
	// One distinctive shape per icon, so a typo in an Icon key (which would
	// silently fall back to the plain document) is caught.
	marks := map[EmailIcon]string{
		IconEnvelope:  `M11 17l21 16 21-16`,
		IconWorkspace: `stroke-dasharray="4 3.2"`,
		IconTeam:      `<circle cx="30" cy="16" r="7"`,
		IconLock:      `M20 28v-7a10 10 0 0 1 20 0v7`,
		IconPortal:    `M26 54h14M33 44v10`,
		IconDocPlus:   `M52 41v10M47 46h10`,
		IconDocCheck:  `M47 46l3.5 3.5 6.5-7`,
		IconDocArrow:  `M44 38h10v-7l12 12-12 12v-7H44z`,
		IconDocClock:  `M52 40v6l4 3`,
		IconWave:      `rotate(-18 36 34)`,
		IconPlane:     `M8 30L64 10 48 54 36 38z`,
	}
	for icon, mark := range marks {
		t.Run(string(icon), func(t *testing.T) {
			e := sampleEmail()
			e.Icon = icon
			assert.Contains(t, mustRender(t, e), mark)
		})
	}
}

func TestRenderEmail_HeaderAndBanner(t *testing.T) {
	withTestEmailBrand(t)
	out := mustRender(t, sampleEmail())

	assert.Contains(t, out, `aria-label="StoneSuite"`, "brand logo")
	assert.Contains(t, out, `<g fill="none" stroke="#BFFF80" stroke-width="5">`, "lime outlined blocks")
	assert.Contains(t, out, `>STONE</text>`)
	assert.Contains(t, out, `>SUITE</text>`)
	assert.Contains(t, out, `fill="#F2EFE8"`, "cream wordmark")
	assert.Contains(t, out, `bgcolor="#001219"`, "dark header so the light logo stays legible")
	assert.Contains(t, out, `<a href="https://app.stonesuite.io/view"`, "view-in-browser target comes from config")
	assert.Contains(t, out, referenceBannerGradient)
	assert.Contains(t, out, `bgcolor="#0a2943"`, "solid fallback for clients that drop CSS gradients")
	assert.Contains(t, out, "background:#1f6b45;border-radius:999px", "filled green pill")
}

func TestRenderEmail_OmitsViewInBrowserWithoutURL(t *testing.T) {
	withTestEmailBrand(t)
	config.AppConfig.EmailViewInBrowserURL = ""

	assert.NotContains(t, mustRender(t, sampleEmail()), "View in browser")
}

func TestRenderEmail_FooterBand(t *testing.T) {
	withTestEmailBrand(t)
	out := mustRender(t, sampleEmail())

	assert.Contains(t, out, "FOLLOW STONESUITE")
	assert.Contains(t, out, `href="https://www.linkedin.com/company/stonesuite"`)
	assert.Contains(t, out, `href="https://x.com/stonesuite"`)
	assert.Contains(t, out, `href="https://www.youtube.com/@stonesuite"`)
	assert.Contains(t, out, `href="mailto:support@stonesuite.app"`, "Need help? line")
	assert.Contains(t, out, `href="https://app.stonesuite.io/settings/notifications"`)
	assert.Contains(t, out, `href="https://app.stonesuite.io/unsubscribe"`)
	assert.Contains(t, out, ">Unsubscribe<")
}

func TestRenderEmail_UnsubscribeFallsBackToPreferences(t *testing.T) {
	withTestEmailBrand(t)
	config.AppConfig.EmailUnsubscribeURL = ""
	out := mustRender(t, sampleEmail())

	assert.Equal(t, 2, strings.Count(out, `href="https://app.stonesuite.io/settings/notifications"`))
}

func TestRenderEmail_SectionOrder(t *testing.T) {
	withTestEmailBrand(t)
	out := mustRender(t, sampleEmail())

	order := []string{`aria-label="StoneSuite"`, referenceBannerGradient, "Hello Alex Approver", ">Invoice Number<", "INV-0042.pdf", ">Review invoice<", "This link expires", "Need help?", "FOLLOW STONESUITE", ">Unsubscribe<"}
	last := -1
	for _, mark := range order {
		i := strings.Index(out, mark)
		require.GreaterOrEqual(t, i, 0, mark)
		assert.Greater(t, i, last, "%q out of order", mark)
		last = i
	}
}

func TestRenderEmail_HasOutlookFixedWidthFallback(t *testing.T) {
	// Outlook desktop (Word engine) ignores CSS max-width; html/template strips
	// literal comments, so the MSO wrapper must survive via the template funcs.
	withTestEmailBrand(t)
	out := mustRender(t, sampleEmail())

	assert.Contains(t, out, `<!--[if mso]><table role="presentation" width="480"`)
	assert.Contains(t, out, `<!--[if mso]></td></tr></table><![endif]-->`)
}

func TestRenderEmail_CTALinkOnlyInHref(t *testing.T) {
	withTestEmailBrand(t)
	e := sampleEmail()
	out := mustRender(t, e)

	assert.Equal(t, 2, strings.Count(out, `href="`+e.ActionURL+`"`), "button and fallback anchor share the destination")
	assert.NotContains(t, out, ">"+e.ActionURL+"<", "and never as visible link text")
}

func TestNotificationRequestEmailHTML_DefaultContentWhenNoEmail(t *testing.T) {
	withTestEmailBrand(t)
	req := NotificationRequest{Title: "Invoice INV-1 sent", Body: "Sent to bob@x.com", Link: "/sales/invoice/1", Channels: []string{"email"}}

	out := mustEmailHTML(t, req)

	assert.Contains(t, out, referenceBannerGradient, "falls back to the shared template, not notify's bare one")
	assert.Contains(t, out, ">NOTIFICATION<")
	assert.Contains(t, out, "Sent to bob@x.com")
	assert.Contains(t, out, `href="https://app.stonesuite.io/sales/invoice/1"`, "relative link made absolute")
}

func TestNotificationRequestEmailHTML_InAppOnlyHasNoEmail(t *testing.T) {
	out := mustEmailHTML(t, NotificationRequest{Title: "x", Channels: []string{"in_app"}})
	assert.Empty(t, out)
}

func TestRenderEmail_EqualGapBetweenEveryBodyBlock(t *testing.T) {
	withTestEmailBrand(t)
	out := mustRender(t, sampleEmail())

	// greeting, paragraph, details, attachment, button, fallback link, fine print
	assert.Equal(t, 7, strings.Count(out, "margin:0 0 16px;"), "every body block is followed by the same 16px gap")
	assert.Contains(t, out, `<p style="margin:0;font-size:13px;color:#52525b;">Need help?`, "the last block adds no trailing gap")
	assert.Contains(t, out, "This link expires in 1 hour.<br>If you didn", "fine-print lines share one block")
	for _, uneven := range []string{"margin:4px 0 18px", "margin:18px 0 0", "margin:6px 0 16px", "margin:0 0 14px", "margin:0 0 4px", "margin:0 0 10px"} {
		assert.NotContains(t, out, uneven)
	}
	assert.Contains(t, out, `<tr><td style="padding:24px;`, "card body padded equally on every side")
}

func TestKeepRefs(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"reference stays on one line", "Your Invoice INV-000123", `Your Invoice <span style="white-space:nowrap;">INV-000123</span>`},
		{"plain text wraps normally", "needs your approval.", "needs your approval."},
		{"escapes markup", "<b>A-1</b>", `<span style="white-space:nowrap;">&lt;b&gt;A-1&lt;/b&gt;</span>`},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, string(keepRefs(tt.in)))
		})
	}
}
