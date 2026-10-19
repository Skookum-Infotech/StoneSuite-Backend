package recordemail

import (
	"html"
	"net/mail"
	"regexp"
	"strings"
)

const (
	// maxBodyChars bounds the stored text so a hostile or huge mail cannot bloat
	// the tenant database.
	maxBodyChars = 20000
	// maxSubjectChars matches the practical limit mail clients enforce.
	maxSubjectChars = 500
	// Bounds on the sender and recipient strings stored with a message, so a
	// crafted header cannot bloat a row.
	maxFromNameChars   = 200
	maxFromEmailChars  = 320
	maxRecipientsChars = 2000
)

// InboundMail is a customer's reply as stonesuite-notify forwards it: the mail
// Resend received, already fetched and merged with the webhook's metadata. This
// service never talks to Resend; notify owns that, and the JSON names below are
// the contract between the two.
type InboundMail struct {
	// EmailID is the provider's id of the mail, the idempotency key: a retried
	// delivery of the same mail is stored once.
	EmailID         string   `json:"emailId"`
	From            string   `json:"from"`
	To              []string `json:"to"`
	CC              []string `json:"cc"`
	BCC             []string `json:"bcc"`
	Subject         string   `json:"subject"`
	MessageID       string   `json:"messageId"`
	InReplyTo       string   `json:"inReplyTo"`
	References      string   `json:"references"`
	Text            string   `json:"text"`
	HTML            string   `json:"html"`
	AttachmentCount int      `json:"attachmentCount"`
}

// Recipients returns every address the mail was sent to (to, cc, bcc), which is
// where a record's reply address can appear: a CC'd or BCC'd copy still arrives
// on our domain.
func (m InboundMail) Recipients() []string {
	out := make([]string, 0, len(m.To)+len(m.CC)+len(m.BCC))
	out = append(out, m.To...)
	out = append(out, m.CC...)
	return append(out, m.BCC...)
}

// BuildMessage turns a received mail into the record's inbound Message: the
// sender's own words (quoted history stripped) as plain text, bounded in size.
func BuildMessage(target Target, in InboundMail) Message {
	fromName, fromEmail := splitFrom(in.From)
	body := in.Text
	if strings.TrimSpace(body) == "" {
		body = HTMLToText(in.HTML)
	}
	body = truncateRunes(StripQuotedReply(body), maxBodyChars)

	return Message{
		RecordID:        target.RecordID,
		WorkflowKey:     target.WorkflowKey,
		Direction:       DirectionIn,
		FromName:        truncateRunes(fromName, maxFromNameChars),
		FromEmail:       truncateRunes(fromEmail, maxFromEmailChars),
		To:              truncateRunes(strings.Join(in.To, ", "), maxRecipientsChars),
		CC:              truncateRunes(strings.Join(in.CC, ", "), maxRecipientsChars),
		Subject:         truncateRunes(in.Subject, maxSubjectChars),
		Body:            body,
		MessageID:       in.MessageID,
		InReplyTo:       in.InReplyTo,
		References:      in.References,
		ProviderEmailID: in.EmailID,
		AttachmentCount: in.AttachmentCount,
	}
}

func splitFrom(raw string) (name, email string) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		return "", strings.TrimSpace(raw)
	}
	return parsed.Name, parsed.Address
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

var (
	reHTMLHead   = regexp.MustCompile(`(?is)<head\b.*?</head>|<(script|style)\b.*?</(script|style)>`)
	reHTMLBlock  = regexp.MustCompile(`(?is)</(p|div|tr|h[1-6]|li|blockquote)>|<br\s*/?>`)
	reHTMLTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText is a deliberately small HTML-to-text for the mail that arrives with
// no text part. It drops head/script/style, turns block ends into line breaks and
// strips the remaining tags; the result is only ever shown as plain text.
func HTMLToText(h string) string {
	s := reHTMLHead.ReplaceAllString(h, "")
	s = reHTMLBlock.ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(strings.Join(strings.Fields(lines[i]), " "))
	}
	return strings.TrimSpace(reBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
