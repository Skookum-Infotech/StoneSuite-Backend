package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

// referenceBannerGradient is the exact banner background of the approved
// "Portal Access" reference design (light header strip + dark gradient
// banner). Every StoneSuite email must carry it -- see WrapEmailHTMLWithBanner.
const referenceBannerGradient = "linear-gradient(135deg,#001219 0%,#005f73 15%,#0a2943 52%,#050d1a 100%)"

func TestBuildOnboardingInviteNotification(t *testing.T) {
	req := buildOnboardingInviteNotification("tenant-1", "invite-1", "owner@example.com", "Jane Owner", "https://app.example/apply?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "owner@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "tenant.onboarding_invited", req.EventType)
	assert.Equal(t, "tenant", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "Onboarding invite email sent.", req.Body)
	assert.Equal(t, "Your StoneSuite Onboarding Invitation", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Jane Owner")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/apply?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the shared banner shell")
	assert.Contains(t, req.EmailBodyHTML, "ONBOARDING INVITE", "banner pill badge")
}

func TestBuildPasswordSetupNotification(t *testing.T) {
	req := buildPasswordSetupNotification("tenant-1", "identity-1", "customer@example.com", "Sam Customer", "https://app.example/set-password?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "customer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "identity.password_setup", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Password setup email sent.", req.Body)
	assert.Equal(t, "Set up your StoneSuite account", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam Customer")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the banner shell")
	assert.Contains(t, req.EmailBodyHTML, "ACCOUNT SETUP", "banner pill badge")
}

func TestBuildUserInviteNotification(t *testing.T) {
	req := buildUserInviteNotification("tenant-1", "invite-1", "user-42", "colleague@example.com", "Alex Colleague", "Acme Stone Co", "https://app.example/accept?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "colleague@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "user-42", req.ActorUserID)
	assert.Equal(t, "user.invited", req.EventType)
	assert.Equal(t, "user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "User invite email sent.", req.Body)
	assert.Equal(t, "You've been invited to Acme Stone Co", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Alex Colleague")
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/accept?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
	// Workspace invite uses the dark banner shell (WrapEmailHTMLWithBanner),
	// not the plain card — see email_layout.go.
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the banner shell")
	assert.Contains(t, req.EmailBodyHTML, "WORKSPACE INVITE", "banner pill badge")
}

func TestBuildPasswordResetNotification(t *testing.T) {
	req := buildPasswordResetNotification("tenant-1", "identity-1", "user@example.com", "Sam User", "https://app.example/reset?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "user@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "identity.password_reset", req.EventType)
	assert.Equal(t, "identity", req.Resource)
	assert.Equal(t, "identity-1", req.ResourceID)
	assert.Equal(t, "Password reset email sent.", req.Body)
	assert.Equal(t, "Reset your StoneSuite password", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Sam User")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/reset?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the shared banner shell")
	assert.Contains(t, req.EmailBodyHTML, "PASSWORD RESET", "banner pill badge")
}

func TestBuildPortalInviteNotification(t *testing.T) {
	req := buildPortalInviteNotification("tenant-1", "invite-1", "staffer@example.com", "Pat Staffer", "Acme Stone Co", "https://app.example/portal-setup?token=abc", 72)

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "staffer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "portal_user.invited", req.EventType)
	assert.Equal(t, "portal_user", req.Resource)
	assert.Equal(t, "invite-1", req.ResourceID)
	assert.Equal(t, "Portal invite email sent.", req.Body)
	assert.Equal(t, "Acme Stone Co — set up your customer portal access", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Pat Staffer")
	assert.Contains(t, req.EmailBodyHTML, "72 hours")
	assert.Contains(t, req.EmailBodyHTML, "https://app.example/portal-setup?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the banner shell")
	assert.Contains(t, req.EmailBodyHTML, "PORTAL ACCESS", "banner pill badge")
}

func TestBuildCustomerPortalInviteNotification(t *testing.T) {
	req := buildCustomerPortalInviteNotification("tenant-1", "42", "buyer@example.com", "Casey Buyer", "Acme Stone Co", "https://portal.example/set-password?token=abc")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "customer_portal.invited", req.EventType)
	assert.Equal(t, "customer_portal", req.Resource)
	assert.Equal(t, "42", req.ResourceID)
	assert.Equal(t, "Customer portal invite email sent.", req.Body)
	assert.Equal(t, "You've been invited to the Acme Stone Co customer portal", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Casey Buyer")
	assert.Contains(t, req.EmailBodyHTML, "https://portal.example/set-password?token=abc")
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the banner shell")
	assert.Contains(t, req.EmailBodyHTML, "PORTAL INVITE", "banner pill badge")
}

func TestBuildCustomerNoteConfirmationNotification(t *testing.T) {
	req := buildCustomerNoteConfirmationNotification("tenant-1", "note-1", "buyer@example.com", "Casey Buyer", "Acme Stone Co")

	assert.Equal(t, "tenant-1", req.TenantID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "buyer@example.com", req.Recipients[0].Email)
	assert.Empty(t, req.Recipients[0].UserID)
	assert.Equal(t, "customer_note.confirmed", req.EventType)
	assert.Equal(t, "customer_note", req.Resource)
	assert.Equal(t, "note-1", req.ResourceID)
	assert.Equal(t, "Note confirmation email sent.", req.Body)
	assert.Equal(t, "Your note to Acme Stone Co was sent", req.Title)
	assert.Contains(t, req.EmailBodyHTML, "Casey Buyer")
	assert.Contains(t, req.EmailBodyHTML, "Acme Stone Co")
	assert.Equal(t, []string{"email"}, req.Channels)
	assert.Contains(t, req.EmailBodyHTML, referenceBannerGradient, "must use the banner shell")
	assert.Contains(t, req.EmailBodyHTML, "RECEIPT CONFIRMED", "banner pill badge")
}

// TestTransactionalEmailBuilders_ContractHolds is a recurrence guard: every
// build*Notification helper must produce a request stonesuite-notify can
// actually deliver as an email. A builder that forgets Channels, EmailBodyHTML,
// or the recipient address ships an email that notify silently drops or
// misroutes -- exactly the class of silent failure this package keeps
// re-introducing. Add a row here whenever a new transactional email is added.
func TestTransactionalEmailBuilders_ContractHolds(t *testing.T) {
	builders := map[string]NotificationRequest{
		"onboarding_invite":      buildOnboardingInviteNotification("t", "id", "to@x.com", "Name", "https://x/apply?token=abc"),
		"password_setup":         buildPasswordSetupNotification("t", "id", "to@x.com", "Name", "https://x/set?token=abc"),
		"user_invite":            buildUserInviteNotification("t", "id", "actor", "to@x.com", "Name", "Workspace", "https://x/accept?token=abc"),
		"password_reset":         buildPasswordResetNotification("t", "id", "to@x.com", "Name", "https://x/reset?token=abc"),
		"portal_invite":          buildPortalInviteNotification("t", "id", "to@x.com", "Name", "Workspace", "https://x/portal?token=abc", 72),
		"customer_portal_invite": buildCustomerPortalInviteNotification("t", "id", "to@x.com", "Name", "Workspace", "https://x/portal?token=abc"),
		"customer_note_confirm":  buildCustomerNoteConfirmationNotification("t", "id", "to@x.com", "Name", "Workspace"),
	}

	for name, req := range builders {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, []string{"email"}, req.Channels, "must target the email channel")
			assert.NotEmpty(t, req.EventType, "EventType drives notify routing/audit")
			assert.NotEmpty(t, req.Resource, "Resource drives notify routing/audit")
			assert.NotEmpty(t, req.ResourceID, "ResourceID correlates the notification")
			assert.NotEmpty(t, req.Title, "Title is the email subject fallback")
			assert.NotEmpty(t, req.EmailBodyHTML, "an empty body ships a blank email")
			require.Len(t, req.Recipients, 1, "transactional emails go to exactly one address")
			assert.NotEmpty(t, req.Recipients[0].Email, "email channel needs a recipient address")
			assert.Empty(t, req.Recipients[0].UserID, "external recipients are addressed by email, not user id")

			// Deliverability guards (see services/email_layout.go): the body
			// must be a complete HTML document, and a tokenized link must
			// never appear as visible body text — a raw "?token=" URL printed
			// next to "set your password" is the phishing pattern Gmail was
			// spam-foldering these on.
			assert.Contains(t, req.EmailBodyHTML, "<!DOCTYPE html>", "body must be a well-formed document, not a bare fragment")
			assert.NotContains(t, req.EmailBodyHTML, "<p>https://", "a raw URL as visible body text is the signal this guards against")
			assert.NotContains(t, req.EmailBodyHTML, "copy and paste this link", "the raw-URL fallback line was removed")
		})
	}
}

func withTestEmailBrand(t *testing.T) {
	t.Helper()
	prev := config.AppConfig
	config.AppConfig = config.Config{
		EmailBrandName:          "StoneSuite",
		SupportEmail:            "support@stonesuite.app",
		EmailPreferencesURL:     "https://app.stonesuite.io/settings/notifications",
		EmailSocialXURL:         "https://x.com/stonesuite",
		EmailSocialInstagramURL: "https://instagram.com/stonesuite",
		EmailViewInBrowserURL:   "https://app.stonesuite.io/view",
		FrontendURL:             "https://app.stonesuite.io/",
	}
	t.Cleanup(func() { config.AppConfig = prev })
}

func TestWrapEmailHTMLWithBanner_WellFormedAndEscapesInputs(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner(`Acme <b>& Co</b>`, `Team <b>Invite</b>`, `You're invited to join`, `Acme <b>Co</b>`, `<p>Hi there</p>`)

	assert.True(t, strings.HasPrefix(out, "<!DOCTYPE html>"), "must open with the doctype")
	assert.Contains(t, out, `<meta charset="utf-8">`)
	assert.Contains(t, out, "<p>Hi there</p>", "inner content is nested verbatim")
	assert.Contains(t, out, "Acme &lt;b&gt;&amp; Co&lt;/b&gt;", "preheader is escaped")
	assert.NotContains(t, out, "Acme <b>& Co</b>")
	assert.Contains(t, out, "TEAM &lt;B&gt;INVITE&lt;/B&gt;", "pill badge label is escaped and uppercased")
	assert.Contains(t, out, "You&#39;re invited to join", "heading line 1 is escaped")
	assert.Contains(t, out, "Acme &lt;b&gt;Co&lt;/b&gt;", "heading line 2 is escaped")
	assert.Contains(t, out, "<svg", "carries the illustrated banner icon (inline SVG, not a hosted image)")
}

func TestBannerIllustration_MatchesReference(t *testing.T) {
	svg := bannerIllustrationSVG

	assert.Contains(t, svg, `<svg width="140" height="112"`, "fixed-size inline SVG, no hosted image")
	assert.Contains(t, svg, `<radialGradient`, "soft lime glow behind the card")
	assert.Contains(t, svg, `fill="#0f2e44" stroke="#c2f589"`, "card: dark fill with lime outline")
	assert.Contains(t, svg, `fill="#445c6d"`, "first placeholder bar in the card")
	assert.Contains(t, svg, `fill="#314c5e"`, "second, darker placeholder bar in the card")
	assert.Contains(t, svg, `stroke="#0a2540"`, "navy checkmark")
	assert.Equal(t, 3, strings.Count(svg, `fill="#c2f589"`), "lime check disc plus two lime sparkles")
	assert.Equal(t, 3, strings.Count(svg, `fill="#9aa3b8"`), "three grey sparkles")
	assert.Contains(t, svg, `rotate(`, "tilted outline square peeking out behind the check disc")
}

func TestWrapEmailHTMLWithBanner_RendersReferenceIllustration(t *testing.T) {
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.Contains(t, out, bannerIllustrationSVG)
}

func TestEmailSocialRow_UsesInstagramIconNotLinkedIn(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.NotContains(t, out, ">in<", "LinkedIn text glyph removed")
	assert.Contains(t, out, `cx="12" cy="12" r="4"`, "Instagram icon: camera lens circle")
}

func TestBrandHeaderStrip_MarkAndWordmarkMatchReference(t *testing.T) {
	withTestEmailBrand(t)
	out := brandHeaderStrip()

	assert.Contains(t, out, `<svg width="19"`, "the reference's tiny logo lockup, as inline SVG rather than a hosted <img>")
	assert.Equal(t, 4, strings.Count(out, `rx="4" fill="none" stroke="#84cc16"`), "four rounded outline squares in the 2x2 grid mark")
	assert.Contains(t, out, ">STONE SUITE<", "live-text wordmark beside the mark")
	assert.Contains(t, out, "color:#1c1917", "near-black wordmark")
	assert.Contains(t, out, "padding:17px 26px", "reference header padding")
}

func TestBrandHeaderStrip_ViewInBrowserLink(t *testing.T) {
	withTestEmailBrand(t)
	out := brandHeaderStrip()

	assert.Contains(t, out, `<a href="https://app.stonesuite.io/view"`, "link target comes from config, not the template")
	assert.Contains(t, out, ">View in browser</a>")
	assert.Contains(t, out, "color:#a8a29e;text-decoration:underline", "muted, underlined, right-aligned link")
}

func TestBrandHeaderStrip_OmitsViewInBrowserWithoutURL(t *testing.T) {
	withTestEmailBrand(t)
	config.AppConfig.EmailViewInBrowserURL = ""
	out := brandHeaderStrip()

	assert.NotContains(t, out, "View in browser", "a link with no destination is worse than no link")
	assert.Contains(t, out, ">STONE SUITE<", "the wordmark still renders")
}

func TestBrandHeaderStrip_EscapesViewInBrowserURL(t *testing.T) {
	withTestEmailBrand(t)
	config.AppConfig.EmailViewInBrowserURL = `https://x.example/v?a=1&b="2"`
	out := brandHeaderStrip()

	assert.Contains(t, out, `href="https://x.example/v?a=1&amp;b=&#34;2&#34;"`)
}

func TestBannerText_DeclaresFontFamily(t *testing.T) {
	// The banner pill and heading once carried no font-family, so they
	// rendered in the client's default serif. Every text run must name the
	// shared sans stack.
	for name, out := range map[string]string{
		"pill":    emailPillBadge("Portal Access"),
		"heading": emailBannerHeading("Your customer", "portal is ready."),
		"header":  brandHeaderStrip(),
	} {
		assert.Contains(t, out, "font-family:"+bodyFont, name)
	}
}

func TestWrapEmailHTMLWithBanner_BannerMatchesReference(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Portal Access", "Your customer", "portal is ready.", "<p>inner</p>")

	assert.Contains(t, out, `height="200"`, "200px-tall banner")
	assert.Contains(t, out, "padding:0 26px", "26px side padding")

	// Three fading lime dots above the pill.
	assert.Equal(t, 3, strings.Count(out, "width:5px;height:5px;border-radius:50%"), "three 5px dots")
	assert.Contains(t, out, "background:#c2f589")
	assert.Contains(t, out, "background:rgba(194,245,137,.44)")
	assert.Contains(t, out, "background:rgba(194,245,137,.18)")

	// Translucent lime pill.
	assert.Contains(t, out, "background:rgba(194,245,137,.12)")
	assert.Contains(t, out, "border:1px solid rgba(194,245,137,.2)")
	assert.Contains(t, out, ">PORTAL ACCESS<")

	// 24px two-tone heading.
	assert.Contains(t, out, "font-size:24px;font-weight:700;line-height:1.15")
	assert.Contains(t, out, "Your customer<br>")
	assert.Contains(t, out, `<span style="color:#c2f589;">portal is ready.</span>`)
}

func TestWrapEmailHTMLWithBanner_HeaderSitsAboveBanner(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	header := strings.Index(out, ">STONE SUITE<")
	banner := strings.Index(out, referenceBannerGradient)
	content := strings.Index(out, "<p>inner</p>")
	require.True(t, header >= 0 && banner >= 0 && content >= 0)
	assert.Less(t, header, banner, "header strip above the banner")
	assert.Less(t, banner, content, "banner above the content card")
}

func TestWrapEmailHTMLWithBanner_PageBackgroundAndUnframedCard(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.Contains(t, out, "background:#edebe8", "warm page background of the reference")
	assert.NotContains(t, out, "border:1px solid #e4e4e7;border-radius:8px", "the reference card has no framing border")
}

func TestWrapEmailHTMLWithBanner_IncludesSupportSocialAndPreferencesFooter(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.Contains(t, out, "support@stonesuite.app", "support contact line")
	assert.Contains(t, out, `href="mailto:support@stonesuite.app"`)
	assert.Contains(t, out, "https://x.com/stonesuite", "social row: X link")
	assert.Contains(t, out, "https://instagram.com/stonesuite", "social row: Instagram link")
	assert.Contains(t, out, `href="https://app.stonesuite.io/settings/notifications"`, "manage-preferences footer link")
	assert.Contains(t, out, "Manage email preferences")
}

func TestWrapEmailHTMLWithBanner_HasOutlookFixedWidthFallback(t *testing.T) {
	// Outlook desktop (Word rendering engine) ignores CSS max-width, so a
	// width="100%" table without an MSO-conditional fixed-width fallback
	// stretches to the full reading-pane width instead of staying a 480px
	// card.
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.Contains(t, out, "<!--[if mso]>", "MSO-conditional fixed-width wrapper")
	assert.Contains(t, out, "width=\"480\"")
}

func TestWrapEmailHTMLWithBanner_GradientBannerHasBgcolorFallback(t *testing.T) {
	// CSS gradients silently fail in Outlook (Word engine) — without a solid
	// bgcolor fallback, the banner renders with no background at all, making
	// the white heading text invisible against a white card.
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.Contains(t, out, `bgcolor="#0a2943"`)
	assert.Contains(t, out, referenceBannerGradient)
}

func TestEmailCTAAccent_LinkOnlyInHref(t *testing.T) {
	const link = "https://app.example/accept-invite?token=secret"
	out := emailCTAAccent(link, "Accept invitation")

	assert.Contains(t, out, `href="`+link+`"`, "the destination lives in the anchor href")
	assert.NotContains(t, out, ">"+link+"<", "and never as visible link text")
	assert.Contains(t, out, "Accept invitation")
	assert.Contains(t, out, "#84cc16", "uses the lime accent color")
}

func TestEmailAttachmentChip_EscapesFileName(t *testing.T) {
	out := EmailAttachmentChip(`INV<1>.pdf`)
	assert.Contains(t, out, "INV&lt;1&gt;.pdf")
}

func TestEmailMessageBox_WrapsInnerInLightGrayBox(t *testing.T) {
	out := EmailMessageBox(`<p>hi</p>`)
	assert.Contains(t, out, "background:#f4f4f5")
	assert.Contains(t, out, "border-radius:8px")
	assert.Contains(t, out, "<p>hi</p>")
}

func TestWrapEmailHTMLWithBanner_HasDividerBeforeFooterChrome(t *testing.T) {
	withTestEmailBrand(t)
	out := WrapEmailHTMLWithBanner("preheader", "Badge", "Line one", "", "<p>inner</p>")

	assert.Contains(t, out, "border-top:1px solid #e4e4e7", "divider between content and footer chrome")
}

func TestSendUserInviteEmailWithResult_ParsesIDsAndSetsActor(t *testing.T) {
	var got NotificationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"notifications":[{"id":"n-1"}]}}`))
	}))
	defer server.Close()
	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendUserInviteEmailWithResult(context.Background(),
		"tenant-1", "invite-1", "user-42",
		"colleague@example.com", "Alex Colleague", "Acme Stone Co",
		"https://app.example/accept?token=abc")

	require.NoError(t, err)
	assert.Equal(t, []string{"n-1"}, res.NotificationIDs)
	assert.Equal(t, "user-42", got.ActorUserID)
	assert.Equal(t, "user.invited", got.EventType)
	require.Len(t, got.Recipients, 1)
	assert.Equal(t, "colleague@example.com", got.Recipients[0].Email)
	assert.Empty(t, got.Recipients[0].UserID)
}

func TestSendUserInviteEmailWithResult_UnparseableBodyIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated) // empty body: notify accepted, no ids echoed
	}))
	defer server.Close()
	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}

	res, err := SendUserInviteEmailWithResult(context.Background(),
		"tenant-1", "invite-1", "user-42",
		"colleague@example.com", "Alex Colleague", "Acme Stone Co",
		"https://app.example/accept?token=abc")

	require.NoError(t, err)
	assert.Empty(t, res.NotificationIDs)
}

func TestEmailPillBadge_MatchesReferenceGeometry(t *testing.T) {
	out := emailPillBadge("Portal Access")

	assert.Contains(t, out, "line-height:12px", "12px line box")
	assert.Contains(t, out, "padding:3px 10px", "with the padding gives the reference's 20px-tall pill")
	assert.Contains(t, out, "border-radius:999px", "fully rounded")
	assert.Contains(t, out, "<table", "a table cell, not an inline-block whose line box adds unpredictable slack")
}

func TestBannerIllustration_KeepsGlowInsideItsViewBox(t *testing.T) {
	// The glow once extended past the viewBox and was cropped flat at the
	// bottom edge. Its circle must fit inside 140x112.
	assert.Contains(t, bannerIllustrationSVG, `<circle cx="70" cy="58" r="54" fill="url(#ssBannerGlow)"/>`)
}

func TestEmailFooterChrome_DeclaresFontFamily(t *testing.T) {
	// The social row and preferences line sit outside the content card (the
	// only cell that carried the shared font stack), so they rendered in the
	// client's default serif.
	withTestEmailBrand(t)

	assert.Contains(t, emailSocialRow(), "font-family:"+bodyFont, "social row")
	assert.Contains(t, emailPreferencesFooter(), "font-family:"+bodyFont, "preferences line")
}
