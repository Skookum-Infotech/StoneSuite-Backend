package services

import (
	"html"
	"strings"

	"stonesuite-backend/config"
)

// Palette of the approved header/banner reference design. Every StoneSuite
// email shares it through WrapEmailHTMLWithBanner; only the text varies.
const (
	bannerLime       = "#c2f589"
	bannerGrey       = "#9aa3b8"
	bannerGradient   = "linear-gradient(135deg,#001219 0%,#005f73 15%,#0a2943 52%,#050d1a 100%)"
	bannerFallbackBG = "#0a2943" // solid stand-in for clients (Outlook) that drop CSS gradients
	bannerHeightPx   = "200"
	wordmarkColor    = "#1c1917"
	viewLinkColor    = "#a8a29e"
	pageBackground   = "#edebe8"
)

// bannerIllustrationSVG is the banner's right-hand illustration: a card with a
// lime check disc overlapping its top edge, a tilted outline square behind it,
// a soft lime glow and five sparkles (two lime, three grey). Inline SVG so it
// never depends on a hosted image (see WrapEmailHTML's doc comment on why this
// package avoids remote <img> logos). Gmail and Outlook desktop strip inline
// SVG, so there the banner simply shows its text without the illustration.
const bannerIllustrationSVG = `<svg width="140" height="112" viewBox="0 0 140 112" xmlns="http://www.w3.org/2000/svg" role="presentation" style="display:block;">` +
	`<defs><radialGradient id="ssBannerGlow" cx="70" cy="58" r="54" gradientUnits="userSpaceOnUse">` +
	`<stop offset="0" stop-color="` + bannerLime + `" stop-opacity=".22"/>` +
	`<stop offset="1" stop-color="` + bannerLime + `" stop-opacity="0"/></radialGradient></defs>` +
	`<circle cx="70" cy="58" r="54" fill="url(#ssBannerGlow)"/>` +
	`<rect x="34" y="19.5" width="52" height="52" rx="9" fill="none" stroke="` + bannerLime + `" stroke-opacity=".35" stroke-width="1.5" transform="rotate(11 60 45.5)"/>` +
	`<rect x="21.5" y="37" width="97" height="54" rx="9" fill="#0f2e44" stroke="` + bannerLime + `" stroke-width="1.6"/>` +
	`<rect x="38" y="71.6" width="63.5" height="4.7" rx="2.3" fill="#445c6d"/>` +
	`<rect x="38" y="81.2" width="41" height="4.5" rx="2.2" fill="#314c5e"/>` +
	`<circle cx="70" cy="44" r="24" fill="` + bannerLime + `"/>` +
	`<path d="M57.5 43.8L66 52.3L80.2 35.9" fill="none" stroke="#0a2540" stroke-width="5.5" stroke-linecap="round" stroke-linejoin="round"/>` +
	`<path d="M15 8.1Q16.3 15.3 23.5 16.6Q16.3 17.9 15 25.1Q13.7 17.9 6.5 16.6Q13.7 15.3 15 8.1Z" fill="` + bannerLime + `"/>` +
	`<path d="M129 75Q130.3 82.4 137.7 83.7Q130.3 85 129 92.4Q127.7 85 120.3 83.7Q127.7 82.4 129 75Z" fill="` + bannerLime + `"/>` +
	`<path d="M125.4 4.2Q126.3 9.3 131.4 10.2Q126.3 11.1 125.4 16.2Q124.5 11.1 119.4 10.2Q124.5 9.3 125.4 4.2Z" fill="` + bannerGrey + `"/>` +
	`<path d="M10.4 86.3Q11.2 90.8 15.7 91.6Q11.2 92.4 10.4 96.9Q9.6 92.4 5.1 91.6Q9.6 90.8 10.4 86.3Z" fill="` + bannerGrey + `"/>` +
	`<path d="M111 103Q111.6 106.4 115 107Q111.6 107.6 111 111Q110.4 107.6 107 107Q110.4 106.4 111 103Z" fill="` + bannerGrey + `"/>` +
	`</svg>`

// brandLogoSVG is the StoneSuite logo lockup: a 2x2 grid of rounded outline
// squares plus the "STONE / SUITE" wordmark, recreated as inline SVG from the
// brand asset. The reference header shows it at ~19px wide beside the live-text
// "STONE SUITE" wordmark, so it is deliberately tiny — the visible brand name
// is the text, not this mark. Decorative for assistive tech (role=presentation)
// because the adjacent text already carries the name.
const brandLogoSVG = `<svg width="19" height="7" viewBox="0 0 112 40" xmlns="http://www.w3.org/2000/svg" role="presentation" style="vertical-align:middle;">` +
	`<rect x="0" y="2" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<rect x="20" y="2" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<rect x="0" y="22" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<rect x="20" y="22" width="16" height="16" rx="4" fill="none" stroke="#84cc16" stroke-width="3"/>` +
	`<text x="46" y="15" font-family="Arial,Helvetica,sans-serif" font-size="13" font-weight="700" letter-spacing="1" fill="#3f3f46">STONE</text>` +
	`<text x="46" y="33" font-family="Arial,Helvetica,sans-serif" font-size="13" font-weight="700" letter-spacing="1" fill="#3f3f46">SUITE</text>` +
	`</svg>`

// brandHeaderStrip is the white header row above every banner: the logo
// lockup and "STONE SUITE" wordmark on the left, and a "View in browser" link
// (config.AppConfig.EmailViewInBrowserURL) on the right. The link is omitted
// when no URL is configured — a link with no destination is worse than none.
func brandHeaderStrip() string {
	link := ""
	if url := config.AppConfig.EmailViewInBrowserURL; url != "" {
		link = `<td align="right" valign="middle" style="font-family:` + bodyFont + `;font-size:10px;line-height:13px;">` +
			`<a href="` + html.EscapeString(url) + `" style="color:` + viewLinkColor + `;text-decoration:underline;">View in browser</a></td>`
	}
	return `<tr><td style="padding:17px 26px;background:#ffffff;">` +
		`<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr>` +
		`<td valign="middle" style="font-size:11px;line-height:13px;">` + brandLogoSVG +
		`<span style="font-family:` + bodyFont + `;font-size:11px;font-weight:700;letter-spacing:.02em;color:` + wordmarkColor + `;vertical-align:middle;margin-left:7px;">STONE SUITE</span></td>` +
		link + `</tr></table></td></tr>`
}

// emailPillBadge renders the small rounded label chip shown under the banner's
// dots (e.g. "PORTAL ACCESS"). label is caller-supplied and escaped. Uppercased
// in Go, not just via CSS text-transform, so the badge still reads correctly in
// a plain-text/no-CSS render of the markup.
func emailPillBadge(label string) string {
	return `<table role="presentation" cellspacing="0" cellpadding="0"><tr><td style="font-family:` + bodyFont + `;font-size:9px;font-weight:700;line-height:12px;letter-spacing:.03em;color:` + bannerLime +
		`;background:rgba(194,245,137,.12);border:1px solid rgba(194,245,137,.2);border-radius:999px;padding:3px 10px;">` +
		html.EscapeString(strings.ToUpper(label)) + `</td></tr></table>`
}

// emailBannerHeading renders the two-tone heading under the pill: line1 in
// white, line2 (optional) in the lime accent. Both are caller-supplied and
// escaped.
func emailBannerHeading(line1, line2 string) string {
	out := `<div style="font-family:` + bodyFont + `;font-size:24px;font-weight:700;line-height:1.15;color:#ffffff;margin:11px 0 0;">` + html.EscapeString(line1)
	if line2 != "" {
		out += `<br><span style="color:` + bannerLime + `;">` + html.EscapeString(line2) + `</span>`
	}
	return out + `</div>`
}

// bannerDots renders the three fading lime dots (5px, 9px pitch) that sit above
// the pill. Table cells rather than inline-block spans: cell backgrounds and
// border-radius survive more clients than sized inline elements.
func bannerDots() string {
	dot := func(bg string) string {
		return `<td width="5" height="5" style="width:5px;height:5px;border-radius:50%;background:` + bg + `;font-size:0;line-height:0;">&nbsp;</td>`
	}
	gap := `<td width="4" style="font-size:0;line-height:0;">&nbsp;</td>`
	return `<table role="presentation" cellspacing="0" cellpadding="0"><tr>` +
		dot(bannerLime) + gap + dot("rgba(194,245,137,.44)") + gap + dot("rgba(194,245,137,.18)") +
		`</tr></table>`
}

// bannerSpacer is the vertical gap between the dots and the pill, as a sized
// block rather than a margin (margins on tables are dropped by some clients).
const bannerSpacer = `<div style="height:8px;line-height:8px;font-size:0;">&nbsp;</div>`

// bannerRow is the 200px gradient banner: dots, pill and two-tone heading on
// the left, the illustration on the right, both vertically centered. The solid
// bgcolor is the fallback for clients that drop CSS gradients — without it the
// white heading would sit on the white card and vanish.
func bannerRow(badgeLabel, headingLine1, headingLine2 string) string {
	return `<tr><td bgcolor="` + bannerFallbackBG + `" height="` + bannerHeightPx + `" valign="middle" style="background:` + bannerGradient + `;height:` + bannerHeightPx + `px;padding:0 26px;">` +
		`<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr>` +
		`<td valign="middle" style="padding:6px 0 0;">` + bannerDots() + bannerSpacer + emailPillBadge(badgeLabel) + emailBannerHeading(headingLine1, headingLine2) + `</td>` +
		`<td width="140" valign="middle" align="right" style="padding:0 6px 4px 0;">` + bannerIllustrationSVG + `</td>` +
		`</tr></table></td></tr>`
}
