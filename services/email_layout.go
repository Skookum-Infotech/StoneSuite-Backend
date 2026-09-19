package services

import (
	"html"
	"strings"

	"stonesuite-backend/config"
)

// bodyFont is the font stack shared by every content card in this package.
const bodyFont = `-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`

// bannerIconSVG is the illustrated icon on every StoneSuite-branded banner
// (WrapEmailHTMLWithBanner): a monitor/laptop glyph with a glowing checkmark
// badge overlapping its corner and a few sparkle accents, matching the
// reference design's illustration style. Inline so it never depends on a
// hosted image — see WrapEmailHTML's doc comment on why this package avoids
// remote <img> logos. The glow is an SVG blur filter, which degrades
// gracefully (plain circle, no blur) in any renderer that ignores <filter>.
const bannerIconSVG = `<svg width="64" height="64" viewBox="0 0 80 80" xmlns="http://www.w3.org/2000/svg" role="presentation">` +
	`<defs><filter id="bannerIconGlow" x="-60%" y="-60%" width="220%" height="220%">` +
	`<feGaussianBlur stdDeviation="4"/></filter></defs>` +
	`<path d="M10 6l1.6 3.8L16 11l-4.4 1.7L10 17l-1.6-4.3L4 11l4.4-1.2z" fill="#a3e635"/>` +
	`<path d="M70 46l1.1 2.6 2.9 1.1-2.9 1.1L70 54l-1.1-3.2L66 49.7l2.9-1.1z" fill="#a3e635"/>` +
	`<path d="M66 12l0.9 2 2 0.9-2 0.9-0.9 2-0.9-2-2-0.9 2-0.9z" fill="#a3e635"/>` +
	`<rect x="6" y="26" width="46" height="32" rx="6" fill="#0f172a" stroke="#52525b" stroke-width="1.5"/>` +
	`<line x1="14" y1="42" x2="44" y2="42" stroke="#71717a" stroke-width="2.5" stroke-linecap="round"/>` +
	`<circle cx="53" cy="30" r="21" fill="#84cc16" opacity="0.35" filter="url(#bannerIconGlow)"/>` +
	`<circle cx="53" cy="30" r="15" fill="#84cc16"/>` +
	`<path d="M46 30l5 5 9-10" stroke="#052e16" stroke-width="3" fill="none" stroke-linecap="round" stroke-linejoin="round"/>` +
	`</svg>`

// emailPillBadge renders the small rounded label chip shown at the top of a
// banner (e.g. "WORKSPACE INVITE"). label is caller-supplied and escaped.
// Uppercased in Go, not just via CSS text-transform, so the badge still
// reads correctly in a plain-text/no-CSS render of the markup.
func emailPillBadge(label string) string {
	return `<span style="display:inline-block;background:rgba(132,204,22,.16);color:#a3e635;font-size:11px;font-weight:700;letter-spacing:.06em;padding:4px 10px;border-radius:999px;">` +
		html.EscapeString(strings.ToUpper(label)) + `</span>`
}

// emailBannerHeading renders the two-tone heading under a banner's pill
// badge: line1 in white, line2 (optional) in the lime accent color. Both are
// caller-supplied and escaped.
func emailBannerHeading(line1, line2 string) string {
	out := `<div style="font-size:19px;font-weight:700;line-height:1.35;margin-top:10px;color:#ffffff;">` + html.EscapeString(line1)
	if line2 != "" {
		out += `<br><span style="color:#a3e635;">` + html.EscapeString(line2) + `</span>`
	}
	return out + `</div>`
}

// brandLogoSVG is the StoneSuite logo mark: a 2x2 grid of rounded outline
// squares plus the "STONE / SUITE" wordmark, recreated as inline SVG from
// the brand asset (never a hosted <img> — see WrapEmailHTML's doc comment on
// why this package avoids remote logos). The source asset uses light text
// for a dark background; here the wordmark is dark (#3f3f46) because this
// mark sits on the light header strip, not the dark banner.
const brandLogoSVG = `<svg width="112" height="40" viewBox="0 0 112 40" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="StoneSuite">` +
	`<rect x="0" y="2" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<rect x="20" y="2" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<rect x="0" y="22" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<rect x="20" y="22" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<text x="46" y="15" font-family="Arial,Helvetica,sans-serif" font-size="13" font-weight="700" letter-spacing="1" fill="#3f3f46">STONE</text>` +
	`<text x="46" y="33" font-family="Arial,Helvetica,sans-serif" font-size="13" font-weight="700" letter-spacing="1" fill="#3f3f46">SUITE</text>` +
	`</svg>`

// brandHeaderStrip is the light header row above every StoneSuite-branded
// banner: the StoneSuite logo mark (brandLogoSVG).
func brandHeaderStrip() string {
	return `<tr><td style="padding:20px 32px 0;">` + brandLogoSVG + `</td></tr>`
}

// EmailMessageBox wraps the primary message paragraph of a template in the
// rounded light-gray box every template uses to set the main message off
// from the greeting above and the CTA below. inner is trusted HTML (already
// built by the caller via emailParagraph/html.EscapeString), not raw text.
// Exported for controllers/documents.go and controllers/crm.go, which build
// their own inner content outside this package.
func EmailMessageBox(inner string) string {
	return `<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="margin:0 0 14px;"><tr><td style="background:#f4f4f5;border-radius:8px;padding:14px 16px;">` +
		inner + `</td></tr></table>`
}

// emailDivider is the thin rule between a template's content/CTA section and
// its footer chrome (support/social/preferences).
func emailDivider() string {
	return `<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td style="padding:18px 32px 0;"><div style="border-top:1px solid #e4e4e7;line-height:1px;font-size:1px;">&nbsp;</div></td></tr></table>`
}

// emailSupportLine is the "Got questions?" contact line shown under a
// StoneSuite-branded banner email's content. The address is a placeholder
// (config.AppConfig.SupportEmail) pending a real support inbox.
func emailSupportLine() string {
	addr := config.AppConfig.SupportEmail
	return `<p style="font-size:13px;color:#71717a;margin:18px 0 0;">Got questions? Reach out anytime at <a href="mailto:` +
		addr + `" style="color:#3f6212;">` + html.EscapeString(addr) + `</a>.</p>`
}

// instagramIconSVG is a minimal camera-outline glyph (rounded square + lens
// circle + shutter dot) used in emailSocialRow — the standard iconography
// for Instagram, not the actual Instagram wordmark/asset.
const instagramIconSVG = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle;">` +
	`<rect x="3" y="3" width="18" height="18" rx="5" stroke="#52525b" stroke-width="2"/>` +
	`<circle cx="12" cy="12" r="4" stroke="#52525b" stroke-width="2"/>` +
	`<circle cx="17.5" cy="6.5" r="1.2" fill="#52525b"/>` +
	`</svg>`

// emailSocialRow is the "FOLLOW <brand>" row of social icon links: X
// (Twitter) and Instagram. config.AppConfig.EmailSocialXURL/
// EmailSocialInstagramURL are placeholders pending real social accounts.
func emailSocialRow() string {
	brand := html.EscapeString(strings.ToUpper(config.AppConfig.EmailBrandName))
	icon := func(href, content string) string {
		return `<a href="` + href + `" style="display:inline-block;width:28px;height:28px;line-height:28px;border-radius:50%;background:#f4f4f5;color:#52525b;font-size:12px;font-weight:700;text-decoration:none;text-align:center;margin:0 4px;">` + content + `</a>`
	}
	return `<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td style="padding:20px 32px 0;text-align:center;">` +
		`<div style="font-size:11px;font-weight:700;letter-spacing:.08em;color:#a1a1aa;margin-bottom:10px;">FOLLOW ` + brand + `</div>` +
		icon(config.AppConfig.EmailSocialXURL, "X") + icon(config.AppConfig.EmailSocialInstagramURL, instagramIconSVG) +
		`</td></tr></table>`
}

// emailPreferencesFooter is the trailing "you're receiving this because..."
// line with a "manage preferences" link. config.AppConfig.EmailPreferencesURL
// is a placeholder pending a real preferences page.
func emailPreferencesFooter() string {
	brand := html.EscapeString(config.AppConfig.EmailBrandName)
	return `<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td style="padding:14px 32px 24px;text-align:center;font-size:11px;color:#a1a1aa;">` +
		`You're receiving this because you have a ` + brand + ` account. ` +
		`<a href="` + config.AppConfig.EmailPreferencesURL + `" style="color:#71717a;">Manage email preferences</a>.` +
		`</td></tr></table>`
}

// mso480Open/mso480Close wrap the 480px content block (card + footer rows) in
// an MSO-conditional fixed-width table. Outlook desktop (the Word rendering
// engine) ignores CSS max-width, so without this a width="100%" table
// stretches to the full reading-pane width instead of staying a 480px card —
// every other client ignores the comment and just sees the fluid table.
const mso480Open = `<!--[if mso]><table role="presentation" width="480" align="center" cellspacing="0" cellpadding="0"><tr><td><![endif]-->`
const mso480Close = `<!--[if mso]></td></tr></table><![endif]-->`

// WrapEmailHTMLWithBanner is the ONE shared shell every StoneSuite email
// template renders through — StoneSuite-account emails and tenant-context
// emails (document delivery, customer welcome) alike. It is: a light header
// strip (StoneSuite mark + wordmark — always StoneSuite, since StoneSuite is
// the platform sending every email; a tenant's identity appears in the
// content text instead, e.g. "Acme Stone Co has given you access..." — see
// docpdf.Seller.LogoPNG's doc comment for why there is no tenant-logo asset
// to swap in here), a dark navy/teal gradient banner (pill badge, two-tone
// heading, illustrated icon), the white content card, a divider, then the
// standard footer (support line, social row, manage-preferences link).
// badgeLabel/headingLine1/headingLine2 are caller-supplied and escaped.
// There is no second, differently-shaped shell — every template's visual
// structure is identical; only these text arguments and inner vary.
func WrapEmailHTMLWithBanner(preheader, badgeLabel, headingLine1, headingLine2, inner string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light only">
<title>StoneSuite</title>
</head>
<body style="margin:0;padding:0;background:#f4f4f5;">
<div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">` + html.EscapeString(preheader) + `</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#f4f4f5;">
<tr><td align="center" style="padding:24px 12px;">
` + mso480Open + `
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:480px;border:1px solid #e4e4e7;border-radius:8px;background:#ffffff;">
` + brandHeaderStrip() + `
<tr><td bgcolor="#0f172a" style="background:linear-gradient(135deg,#0f172a,#134e4a);padding:18px 32px 22px;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr>
<td valign="top">` + emailPillBadge(badgeLabel) + emailBannerHeading(headingLine1, headingLine2) + `</td>
<td width="72" valign="top" align="right">` + bannerIconSVG + `</td>
</tr></table>
</td></tr>
<tr><td style="padding:28px 32px;font-family:` + bodyFont + `;font-size:15px;line-height:1.55;color:#27272a;">
` + inner + emailSupportLine() + `
</td></tr>
<tr><td>` + emailDivider() + `</td></tr>
</table>
` + emailSocialRow() + emailPreferencesFooter() + mso480Close + `
</td></tr></table>
</body>
</html>`
}

// emailCTAAccent renders the primary call-to-action button plus a single
// fallback link, in the lime accent color used by every StoneSuite template.
// The destination never appears as visible URL text: a raw tokenized URL
// printed in the body next to "set your password" is the strongest phishing
// signal a transactional email can carry, and the reason workspace invites
// were being spam-foldered. Both anchors carry the same href.
func emailCTAAccent(url, label string) string {
	return `<p style="margin:24px 0;"><a href="` + url + `" style="display:inline-block;background:#84cc16;color:#052e16;text-decoration:none;padding:11px 22px;border-radius:6px;font-weight:700;">` + label + `</a></p>` +
		`<p style="font-size:13px;color:#71717a;">Button not working? <a href="` + url + `" style="color:#3f6212;">Use this link instead</a>.</p>`
}

// EmailAttachmentChip renders a small file-pill for an attached document
// (e.g. the Document Delivered email). fileName is caller-supplied and
// escaped. Exported for controllers/documents.go, which builds its own inner
// content outside this package.
func EmailAttachmentChip(fileName string) string {
	return `<table role="presentation" cellspacing="0" cellpadding="0" style="margin:4px 0 14px;"><tr><td style="background:#f4f4f5;border:1px solid #e4e4e7;border-radius:6px;padding:8px 12px;font-size:13px;color:#3f3f46;">📄 ` +
		html.EscapeString(fileName) + `</td></tr></table>`
}

// emailParagraph is a plain body paragraph with the shell's default spacing.
func emailParagraph(html string) string {
	return `<p style="margin:0 0 14px;">` + html + `</p>`
}

// emailFinePrint is the muted trailing note (expiry, "ignore this email").
func emailFinePrint(text string) string {
	return `<p style="font-size:13px;color:#71717a;margin:14px 0 0;">` + text + `</p>`
}
