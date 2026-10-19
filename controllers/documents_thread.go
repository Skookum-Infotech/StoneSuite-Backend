package controllers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/recordemail"
	"stonesuite-backend/services"
	"stonesuite-backend/userstore"
)

// recordThreads is the part of RecordEmailOps the document-send path needs: the
// per-record Reply-To that makes a customer's reply come back to the record.
type recordThreads interface {
	ReplyAddress(ctx context.Context, tenantID, recordID, workflowKey string) (string, bool)
}

// WithRecordEmail enables reply routing for document sends (see routeReplies)
// and returns h. Without it a send goes out exactly as it always did.
func (h *DocumentOps) WithRecordEmail(t recordThreads) *DocumentOps {
	h.threads = t
	return h
}

// routeReplies points a record's customer email at its Communication thread:
// the Reply-To becomes the record's own address, and the sender reads as the
// rep ("Harish (Acme Stone)") instead of a generic name. It returns the sending
// user for recordSent. A record type without a thread, or a feature that is not
// set up, leaves req untouched.
func (h *DocumentOps) routeReplies(
	ctx context.Context, pool *pgxpool.Pool, req *services.NotificationRequest, p *preparedSend, identityID string,
) userstore.User {
	if !threadWorkflows[p.meta.WorkflowKey] {
		return userstore.User{}
	}
	actor := loadActor(ctx, pool, identityID)
	if h.threads == nil {
		return actor
	}
	if addr, ok := h.threads.ReplyAddress(ctx, p.tenantID, p.recordID, p.meta.WorkflowKey); ok {
		req.EmailReplyTo = addr
		req.EmailFromName = senderLabel(actor.FullName, p.doc.Seller.Name)
	}
	return actor
}

// recordSent saves the plain-text copy of what the customer was sent as the
// first message of the record's conversation. The send has already happened, so
// a failure here is logged and swallowed rather than reported as a failed send.
func (h *DocumentOps) recordSent(
	ctx context.Context, pool *pgxpool.Pool, p *preparedSend, sendID string, actor userstore.User, email *services.Email,
) {
	if !threadWorkflows[p.meta.WorkflowKey] || email == nil {
		return
	}
	_, err := recordemail.Insert(ctx, pool, recordemail.Message{
		RecordID: p.recordID, WorkflowKey: p.meta.WorkflowKey, Direction: recordemail.DirectionOut,
		FromName: actor.FullName, FromEmail: actor.Email,
		To: strings.Join(p.to, ", "), CC: strings.Join(p.cc, ", "),
		Subject: p.subject, Body: email.PlainText(),
		SentByUserID: actor.ID, DocumentSendID: sendID,
	})
	if err != nil {
		slog.WarnContext(ctx, "document send: conversation copy not saved",
			"workflow", p.meta.WorkflowKey, "record", p.recordID, "send_id", sendID, "error", err)
	}
}
