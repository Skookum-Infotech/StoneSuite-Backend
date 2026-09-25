package controllers

import (
	"context"
	"log/slog"
	"strings"

	"stonesuite-backend/docpdf"
	"stonesuite-backend/services"
)

// documentEmail is the content of a document-send customer email: the seller
// and document number, the attached PDF's card, an optional sender message,
// and a link to the customer portal. It renders through the shared template
// like every other email; the seller's identity appears in the text (its logo
// is on the attached PDF, see docpdf.Seller.LogoPNG).
func documentEmail(d docpdf.PrintableDoc, message, fileName string, pdfSize int) *services.Email {
	kind := docKindLabel(d.Kind)
	portal := services.PortalURL()
	paragraphs := []services.Paragraph{{
		services.Bold(d.Seller.Name), services.Text(" has sent you " + strings.ToLower(kind) + " "),
		services.Link(d.Number, portal),
		services.Text(". It is attached to this email as a PDF. You can also view and download it anytime from your customer portal."),
	}}
	if message != "" {
		paragraphs = append(paragraphs, services.Paragraph{services.Text(message)})
	}
	return &services.Email{
		Preheader:     kind + " " + d.Number + " from " + d.Seller.Name,
		Badge:         "Document Sent",
		Icon:          services.IconDocArrow,
		Heading:       kind + " " + d.Number,
		HeadingAccent: "is on its way.",
		Subtitle:      "Your document is ready to view.",
		Greet:         true,
		Paragraphs:    paragraphs,
		Attachment:    &services.AttachmentCard{FileName: fileName, Size: services.FormatFileSize(pdfSize)},
		ActionURL:     portal,
		ActionLabel:   "View in portal",
	}
}

// docKindLabel title-cases a PrintableDoc kind ("SALES ORDER" -> "Sales Order").
func docKindLabel(kind string) string {
	words := strings.Fields(strings.ToLower(kind))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// sendOwner is the record owner told about a document send: their email,
// control-plane identity id (the id Notify scopes by) and name for the greeting.
type sendOwner struct {
	Email, IdentityID, Name string
}

// notifyOwnerOfSend best-effort-notifies the record's internal owner that
// the document was sent, with the same PDF the customer received attached.
// owner.IdentityID and actorIdentityID are control-plane identity ids (the id
// Notify scopes by), not tenant users.ids. notify is injected (defaults to
// services.SendNotification) so tests don't need a live notify service. A
// failure here is logged and swallowed — the document has already been sent
// and recorded by the time this runs, and a Notify outage must never undo that.
func notifyOwnerOfSend(
	ctx context.Context,
	notify func(context.Context, services.NotificationRequest) error,
	owner sendOwner, tenantID, actorIdentityID string,
	doc docpdf.PrintableDoc, number, workflowKey, recordID string,
	sentTo []string, pdf []byte, fileName string,
) {
	if owner.IdentityID == "" {
		return
	}
	kind := docKindLabel(doc.Kind)
	link := recordLink(workflowKey, recordID)
	sentList := strings.Join(sentTo, ", ")
	err := notify(ctx, services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  []services.RecipientTarget{{UserID: owner.IdentityID, Email: owner.Email, Name: owner.Name}},
		ActorUserID: actorIdentityID,
		EventType:   "document.sent",
		Resource:    workflowKey,
		ResourceID:  recordID,
		Title:       doc.Kind + " " + number + " sent",
		Body:        "Sent to " + sentList,
		Link:        link,
		Channels:    []string{"email"},
		Email: &services.Email{
			Preheader: kind + " " + number + " was sent to " + sentList + ".",
			Badge:     "Document Sent", Icon: services.IconPlane,
			Heading: kind + " " + number, HeadingAccent: "was sent.",
			Subtitle: "A copy has been shared with your customer.",
			Greet:    true,
			Paragraphs: []services.Paragraph{{services.Text(kind + " " + number + " has been sent to " + sentList +
				". A copy of the PDF is below, and you can open the record anytime to check its status.")}},
			Attachment:  &services.AttachmentCard{FileName: fileName, Size: services.FormatFileSize(len(pdf))},
			ActionURL:   services.AppURL(link),
			ActionLabel: "View " + strings.ToLower(kind),
			Reason:      "you're an account owner.",
		},
		Attachments: []services.NotifyAttachment{
			{FileName: fileName, ContentType: "application/pdf", Content: pdf},
		},
	})
	if err != nil {
		slog.WarnContext(ctx, "documents: notify owner of send failed",
			"record_id", recordID, "error", err)
	}
}

// withDownloadLink points the email's PDF card at url so a click downloads the
// PDF; a blank url leaves the card as a plain, unlinked label.
func withDownloadLink(e *services.Email, url string) *services.Email {
	if e.Attachment != nil {
		e.Attachment.URL = url
	}
	return e
}
