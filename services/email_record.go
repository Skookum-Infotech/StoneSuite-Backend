package services

import (
	"html"
	"strings"

	"stonesuite-backend/config"
)

// RecordEmail is the content of a one-record activity email — an approval
// event, a customer-approved notice, or an owner's "document sent" ping. These
// used to fall through to stonesuite-notify's bare generic template; building
// the body here puts them on the same shared shell as every other email. None
// of the callers has a recipient name to greet, so there is no greeting.
type RecordEmail struct {
	Badge      string // banner pill, e.g. "Approval Needed"
	Subject    string // record label, banner heading line 1, e.g. "Invoice INV-000123"
	Verb       string // what happened, banner heading line 2, e.g. "needs your approval"
	Message    string // one plain-text sentence for the message box
	Path       string // app-relative route to the record; empty means no button
	CTA        string // button label, e.g. "Review invoice"
	Attachment string // optional attached file name, shown as a file chip
}

// BuildRecordEmailHTML renders e through WrapEmailHTMLWithBanner. Every
// caller-supplied field is escaped; the button is omitted when there is no
// route to link to (unregistered module, or FrontendURL unset).
func BuildRecordEmailHTML(e RecordEmail) string {
	inner := EmailMessageBox(emailParagraph(html.EscapeString(e.Message)))
	if e.Attachment != "" {
		inner += EmailAttachmentChip(e.Attachment)
	}
	if link := appURL(e.Path); link != "" && e.CTA != "" {
		inner += emailCTAAccent(html.EscapeString(link), html.EscapeString(e.CTA))
	}
	return WrapEmailHTMLWithBanner(e.Subject+" "+e.Verb+".", e.Badge, e.Subject, e.Verb+".", inner)
}

// appURL turns an app-relative route ("/sales/invoice/<id>") into an absolute
// link on the frontend origin. A relative href is dead in an inbox, so an
// empty path or an unset FrontendURL yields "" (no link) rather than a broken one.
func appURL(path string) string {
	base := strings.TrimRight(config.AppConfig.FrontendURL, "/")
	if base == "" || path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
