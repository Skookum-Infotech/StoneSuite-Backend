package services

import (
	"html"
	"strings"
)

// PlainText renders the email's readable content as plain text: the greeting,
// body paragraphs, detail rows, attachment line and call-to-action link, with
// no banner, logo, colours or buttons. It is what a record's Communication
// thread shows for a branded email, so staff read the message itself rather
// than its chrome. Heading/badge/subtitle are deliberately left out — they are
// banner furniture, not part of the conversation.
func (e Email) PlainText() string {
	var blocks []string

	greeting := "Hello,"
	if name := strings.TrimSpace(e.RecipientName); name != "" {
		greeting = "Hello " + name + ","
	}
	if e.Greet {
		blocks = append(blocks, greeting)
	}

	for _, p := range e.Paragraphs {
		if text := paragraphText(p); text != "" {
			blocks = append(blocks, text)
		}
	}

	if e.Code != "" {
		blocks = append(blocks, e.Code)
	}

	if len(e.Details) > 0 {
		rows := make([]string, 0, len(e.Details))
		for _, d := range e.Details {
			rows = append(rows, d.Label+": "+d.Value)
		}
		blocks = append(blocks, strings.Join(rows, "\n"))
	}

	if e.Attachment != nil && e.Attachment.FileName != "" {
		line := "Attachment: " + e.Attachment.FileName
		if e.Attachment.Size != "" {
			line += " (" + e.Attachment.Size + ")"
		}
		blocks = append(blocks, line)
	}

	if e.ActionURL != "" {
		label := e.ActionLabel
		if label == "" {
			label = "Link"
		}
		blocks = append(blocks, label+": "+e.ActionURL)
	}

	if len(e.FinePrint) > 0 {
		blocks = append(blocks, strings.Join(e.FinePrint, "\n"))
	}

	return strings.Join(blocks, "\n\n")
}

// paragraphText joins a paragraph's spans; a link keeps its label only, since
// the same URL is already printed once as the call-to-action line.
func paragraphText(p Paragraph) string {
	var b strings.Builder
	for _, s := range p {
		b.WriteString(s.Text)
	}
	return strings.TrimSpace(b.String())
}

// PlainTextEmailHTML wraps a human-written message as minimal HTML — escaped
// paragraphs and line breaks, no template chrome — for mail that should read
// like a person wrote it (a reply typed into a record's Communication tab).
func PlainTextEmailHTML(text string) string {
	paragraphs := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(paragraphs))
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, "<p>"+strings.ReplaceAll(html.EscapeString(p), "\n", "<br>")+"</p>")
	}
	return "<div>" + strings.Join(out, "") + "</div>"
}
