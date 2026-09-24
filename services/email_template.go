package services

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"
	"sync"

	"stonesuite-backend/config"
)

// emailTemplateFS holds the ONE email template every StoneSuite email is
// rendered through: the full HTML document — header, banner (with its icon
// set), body blocks, CTA and footer — with {{placeholders}} for the per-email
// content. There is no second template; callers only ever supply an Email.
//
//go:embed templates/email.html
var emailTemplateFS embed.FS

const emailTemplateFile = "templates/email.html"

// defaultEmailReason completes the footer's "You're receiving this because ..."
// when an Email does not set its own Reason.
const defaultEmailReason = "you have a StoneSuite account."

// emailTemplateFuncs are the helpers the template calls. msoOpen/msoClose emit
// Outlook-desktop conditional comments (html/template strips comments written
// literally in a template): Outlook's Word engine ignores CSS max-width, so
// without a fixed-width 480px wrapper the card stretches to the reading pane.
var emailTemplateFuncs = template.FuncMap{
	"upper": strings.ToUpper,
	"msoOpen": func() template.HTML {
		return `<!--[if mso]><table role="presentation" width="480" align="center" cellspacing="0" cellpadding="0"><tr><td><![endif]-->`
	},
	"msoClose": func() template.HTML { return `<!--[if mso]></td></tr></table><![endif]-->` },
}

// loadEmailTemplate parses the embedded template once, on first use.
var loadEmailTemplate = sync.OnceValues(func() (*template.Template, error) {
	return template.New("email.html").Funcs(emailTemplateFuncs).ParseFS(emailTemplateFS, emailTemplateFile)
})

// EmailIcon selects the banner illustration drawn by the shared template.
type EmailIcon string

// Banner illustrations available in the shared template.
const (
	IconEnvelope  EmailIcon = "envelope"
	IconWorkspace EmailIcon = "workspace"
	IconTeam      EmailIcon = "team"
	IconLock      EmailIcon = "lock"
	IconPortal    EmailIcon = "portal"
	IconDocPlus   EmailIcon = "doc-plus"
	IconDocCheck  EmailIcon = "doc-check"
	IconDocArrow  EmailIcon = "doc-arrow"
	IconDocClock  EmailIcon = "doc-clock"
	IconWave      EmailIcon = "wave"
	IconPlane     EmailIcon = "plane"
)

// Span is one run of paragraph text: plain, bold, or a link.
type Span struct {
	Text string
	Bold bool
	Href string
}

// Text is a plain span.
func Text(s string) Span { return Span{Text: s} }

// Bold is an emphasized span (e.g. a company name).
func Bold(s string) Span { return Span{Text: s, Bold: true} }

// Link is a hyperlinked span; an empty href degrades to plain text.
func Link(s, href string) Span { return Span{Text: s, Href: href} }

// Paragraph is one body paragraph made of spans.
type Paragraph []Span

// Detail is one label/value row of the grey details box (e.g. "Amount").
type Detail struct {
	Label string
	Value string
}

// AttachmentCard describes the attached file shown as a PDF card.
type AttachmentCard struct {
	FileName string
	Size     string // human-readable, e.g. "245 KB"
}

// Email is the dynamic content of one email. SendNotification renders it
// through the shared template; every field is plain text and is HTML-escaped
// by the template, so no caller ever builds markup.
type Email struct {
	Preheader     string    // hidden inbox-preview line
	Badge         string    // banner pill, e.g. "Password Reset" (uppercased)
	Icon          EmailIcon // banner illustration; empty draws the plain document
	Heading       string    // banner heading, white line
	HeadingAccent string    // banner heading, lime second line (optional)
	Subtitle      string    // banner sub-line under the heading (optional)
	Greet         bool      // open with "Hello {RecipientName}," ("Hello," when blank)
	RecipientName string    // filled per recipient by SendNotification when left blank
	Paragraphs    []Paragraph
	Details       []Detail        // grey label/value box (optional)
	Attachment    *AttachmentCard // PDF card (optional)
	ActionURL     string          // absolute CTA link; the button is omitted when empty
	ActionLabel   string          // CTA button label
	FinePrint     []string        // muted lines under the CTA, e.g. link expiry
	Reason        string          // footer "You're receiving this because {Reason}"
}

// emailView is Email plus the brand/footer values the template needs from config.
type emailView struct {
	Email
	BrandName, SupportEmail                          string
	PreferencesURL, UnsubscribeURL, ViewInBrowserURL string
	SocialLinkedInURL, SocialXURL, SocialYouTubeURL  string
}

// RenderEmail renders e through the shared email template.
func RenderEmail(e Email) (string, error) {
	tmpl, err := loadEmailTemplate()
	if err != nil {
		return "", fmt.Errorf("parse email template: %w", err)
	}
	if e.Reason == "" {
		e.Reason = defaultEmailReason
	}
	cfg := config.AppConfig
	unsubscribe := cfg.EmailUnsubscribeURL
	if unsubscribe == "" {
		unsubscribe = cfg.EmailPreferencesURL
	}
	v := emailView{
		Email:             e,
		BrandName:         cfg.EmailBrandName,
		SupportEmail:      cfg.SupportEmail,
		PreferencesURL:    cfg.EmailPreferencesURL,
		UnsubscribeURL:    unsubscribe,
		ViewInBrowserURL:  cfg.EmailViewInBrowserURL,
		SocialLinkedInURL: cfg.EmailSocialLinkedInURL,
		SocialXURL:        cfg.EmailSocialXURL,
		SocialYouTubeURL:  cfg.EmailSocialYouTubeURL,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, v); err != nil {
		return "", fmt.Errorf("render email template: %w", err)
	}
	return buf.String(), nil
}

// defaultEmail is the content used for a notification that carries no Email of
// its own, so it still goes out through the shared template instead of
// stonesuite-notify's bare fallback.
func defaultEmail(req NotificationRequest) Email {
	e := Email{
		Preheader:   req.Title,
		Badge:       "Notification",
		Heading:     req.Title,
		ActionURL:   appURL(req.Link),
		ActionLabel: "View in " + config.AppConfig.EmailBrandName,
	}
	if req.Body != "" {
		e.Paragraphs = []Paragraph{{Text(req.Body)}}
	}
	return e
}

// wantsEmail reports whether a request is delivered over email. An empty
// channel list means notify's defaults, which may include email.
func wantsEmail(channels []string) bool {
	if len(channels) == 0 {
		return true
	}
	for _, c := range channels {
		if c == "email" {
			return true
		}
	}
	return false
}

// EmailHTML renders the request's email body through the shared template —
// its Email content, or defaultEmail when none is set. It returns "" for a
// request that is not delivered over email.
func (r NotificationRequest) EmailHTML() (string, error) {
	if r.Email != nil {
		return RenderEmail(*r.Email)
	}
	if !wantsEmail(r.Channels) {
		return "", nil
	}
	return RenderEmail(defaultEmail(r))
}
