package services

import (
	"html"
	"strings"

	"stonesuite-backend/config"
)

// bodyFont is the font stack shared by every content card in this package.
const bodyFont = `-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`

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
	return `<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td style="padding:20px 32px 0;text-align:center;font-family:` + bodyFont + `;">` +
		`<div style="font-size:11px;font-weight:700;letter-spacing:.08em;color:#a1a1aa;margin-bottom:10px;">FOLLOW ` + brand + `</div>` +
		icon(config.AppConfig.EmailSocialXURL, "X") + icon(config.AppConfig.EmailSocialInstagramURL, instagramIconSVG) +
		`</td></tr></table>`
}

// emailPreferencesFooter is the trailing "you're receiving this because..."
// line with a "manage preferences" link. config.AppConfig.EmailPreferencesURL
// is a placeholder pending a real preferences page.
func emailPreferencesFooter() string {
	brand := html.EscapeString(config.AppConfig.EmailBrandName)
	return `<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td style="padding:14px 32px 24px;text-align:center;font-family:` + bodyFont + `;font-size:11px;color:#a1a1aa;">` +
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
// emails (document delivery, customer welcome, record activity) alike. It is,
// top to bottom, exactly the approved reference design: a white header strip
// (logo lockup + "STONE SUITE" wordmark, "View in browser" link) — always
// StoneSuite, since StoneSuite is the platform sending every email; a tenant's
// identity appears in the content text instead, e.g. "Acme Stone Co has given
// you access..." (see docpdf.Seller.LogoPNG's doc comment for why there is no
// tenant-logo asset to swap in here) — a 200px dark gradient banner (fading
// dots, pill badge, two-tone heading, illustration), the white content card, a
// divider, then the standard footer (support line, social row,
// manage-preferences link) on the warm page background.
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
<body style="margin:0;padding:0;background:` + pageBackground + `;">
<div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">` + html.EscapeString(preheader) + `</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" bgcolor="` + pageBackground + `" style="background:` + pageBackground + `;">
<tr><td align="center" style="padding:24px 12px;">
` + mso480Open + `
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:480px;background:#ffffff;">
` + brandHeaderStrip() + `
` + bannerRow(badgeLabel, headingLine1, headingLine2) + `
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
