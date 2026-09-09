package services

import "html"

// WrapEmailHTML wraps transactional body content in a complete, well-formed
// HTML document: DOCTYPE, <head> with charset + viewport, a hidden preheader
// line, and a centred single-column card carrying the StoneSuite wordmark. A
// bare <html><body> fragment and a visible raw URL both push transactional
// mail toward spam filters; every builder in this package and the
// document/welcome emails in controllers/ go through this shell so the
// structure stays consistent and tunable in one place.
func WrapEmailHTML(preheader, inner string) string {
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
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:480px;border:1px solid #e4e4e7;border-radius:8px;background:#ffffff;">
<tr><td style="padding:28px 32px;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.55;color:#27272a;">
<div style="font-weight:700;font-size:17px;color:#18181b;margin-bottom:18px;">StoneSuite</div>
` + inner + `
</td></tr></table></td></tr></table>
</body>
</html>`
}

// emailCTA renders the primary call-to-action button plus a single fallback
// link. The destination never appears as visible URL text: a raw tokenized
// URL printed in the body next to "set your password" is the strongest
// phishing signal a transactional email can carry, and the reason workspace
// invites were being spam-foldered. Both anchors carry the same href.
func emailCTA(url, label string) string {
	return `<p style="margin:24px 0;"><a href="` + url + `" style="display:inline-block;background:#2563eb;color:#ffffff;text-decoration:none;padding:11px 22px;border-radius:6px;font-weight:600;">` + label + `</a></p>` +
		`<p style="font-size:13px;color:#71717a;">Button not working? <a href="` + url + `" style="color:#2563eb;">Use this link instead</a>.</p>`
}

// emailParagraph is a plain body paragraph with the shell's default spacing.
func emailParagraph(html string) string {
	return `<p style="margin:0 0 14px;">` + html + `</p>`
}

// emailHeading is the bold lead line at the top of a message body.
func emailHeading(text string) string {
	return `<p style="font-size:17px;font-weight:600;margin:0 0 14px;">` + text + `</p>`
}

// emailFinePrint is the muted trailing note (expiry, "ignore this email").
func emailFinePrint(text string) string {
	return `<p style="font-size:13px;color:#71717a;margin:14px 0 0;">` + text + `</p>`
}
