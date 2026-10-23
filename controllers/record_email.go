package controllers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/recordemail"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
	"stonesuite-backend/workflow"
)

const (
	// maxReplyChars bounds one reply typed into the Communication tab.
	maxReplyChars = 10000
	// eventRecordEmailReply is the notify event type of a staff reply.
	eventRecordEmailReply = "record.email_reply"
)

// threadKind is how one record type's Communication tab behaves.
type threadKind struct {
	// fallbackSubject heads a reply on a record that has no earlier subject.
	fallbackSubject string
	// party is who is on the other end of the conversation and sendAction the
	// button that starts it; both only word the messages shown to staff.
	party      string
	sendAction string
	// notesOnly keeps a timeline of team notes: nothing is ever emailed, no reply
	// address is minted, and the record has no document to send.
	notesOnly bool
}

const (
	partyCustomer    = "customer"
	partyVendor      = "vendor"
	sendToCustomer   = "Send to customer"
	sendToVendor     = "Send to Vendor"
	notesOnlyRefusal = "This record only keeps internal notes; nothing can be emailed from it."
)

// threadKinds are the record types that keep a conversation. Adding a type here
// is the whole opt-in for the send side, the tab and the reply route; the
// document-send path (see routeReplies) needs the type to have a document loader.
var threadKinds = map[string]threadKind{
	"sales_order":    {fallbackSubject: "Your order", party: partyCustomer, sendAction: sendToCustomer},
	"quote":          {fallbackSubject: "Your quote", party: partyCustomer, sendAction: sendToCustomer},
	"estimate":       {fallbackSubject: "Your estimate", party: partyCustomer, sendAction: sendToCustomer},
	"invoice":        {fallbackSubject: "Your invoice", party: partyCustomer, sendAction: sendToCustomer},
	"purchase_order": {fallbackSubject: "Your purchase order", party: partyVendor, sendAction: sendToVendor},
	"requisition":    {notesOnly: true},
}

// emailThread returns the thread of a record type whose conversation is carried
// by email: the types the send path and the reply address apply to.
func emailThread(workflowKey string) (threadKind, bool) {
	kind, ok := threadKinds[workflowKey]
	return kind, ok && !kind.notesOnly
}

// notifyFunc is services.SendNotificationWithResult, injectable for tests.
type notifyFunc func(context.Context, services.NotificationRequest) (services.NotificationResult, error)

// RecordEmailOps serves a record's Communication tab: the conversation list, a
// staff reply, and the internal route that files a customer's reply. It also
// hands the document-send path the per-record Reply-To (see ReplyAddress).
//
// An instance with no address store or no domain is simply switched off:
// ReplyAddress reports false, so mail goes out exactly as before.
type RecordEmailOps struct {
	addrs  *recordemail.AddressStore
	domain string
	notify notifyFunc
	now    func() time.Time

	// Inbound route wiring (see WithInbound).
	secret  string
	replays *middleware.ReplayGuard
	tenants docLinkTenants
	poolFor func(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error)

	// storageFor is where files are kept, per tenant (see WithStorage); nil keeps none.
	storageFor TenantStorage
}

// NewRecordEmailOps builds the handler group. addrs may be nil and domain empty
// to leave the feature off.
func NewRecordEmailOps(addrs *recordemail.AddressStore, domain string) *RecordEmailOps {
	return &RecordEmailOps{addrs: addrs, domain: domain, notify: services.SendNotificationWithResult, now: time.Now}
}

// Enabled reports whether replies can be routed back to records.
func (h *RecordEmailOps) Enabled() bool { return h != nil && h.addrs != nil && h.domain != "" }

// ReplyAddress returns the Reply-To address for mail about a record, minting the
// record's token on first use. ok is false when the feature is off, the record
// type keeps no email thread, or the token could not be created: the caller then
// sends without a per-record Reply-To rather than failing the send.
func (h *RecordEmailOps) ReplyAddress(ctx context.Context, tenantID, recordID, workflowKey string) (string, bool) {
	if _, ok := emailThread(workflowKey); !h.Enabled() || !ok {
		return "", false
	}
	token, err := h.addrs.TokenFor(ctx, tenantID, recordID, workflowKey)
	if err != nil {
		slog.WarnContext(ctx, "record email: reply address unavailable; sending without one",
			"record", recordID, "error", err)
		return "", false
	}
	return recordemail.Address(token, h.domain), true
}

// List returns a record's email conversation, oldest first. RBAC: <type>:read,
// scoped like the record itself.
func (h *RecordEmailOps) List(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, info, _, ok := authRecordAccess(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	if _, ok := threadKinds[info.WorkflowKey]; !ok {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	msgs, err := recordemail.List(r.Context(), pool, recordID)
	if err != nil {
		slog.ErrorContext(r.Context(), "record email: list failed", "record", recordID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load the conversation.")
		return
	}
	// Who a reply would go to, so the tab can say so before anyone sends: the
	// sender of a customer message is whatever that mail's From header claimed.
	replyTo, err := recordemail.ReplyTarget(r.Context(), pool, recordID)
	if err != nil {
		slog.WarnContext(r.Context(), "record email: reply target unavailable", "record", recordID, "error", err)
	}
	// Whether files can be attached and downloaded: independent of inbound email,
	// since a note can carry files too.
	attachmentsEnabled := false
	if tenant, err := tenancy.TenantFromContext(r.Context()); err == nil {
		attachmentsEnabled = h.storeFor(tenant) != nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "enabled": h.Enabled(), "attachmentsEnabled": attachmentsEnabled,
		"replyTo": replyTo, "messages": msgs,
	})
}

// replyRequest is the body of a Communication-tab message. InternalOnly keeps it
// on the record for the team: nothing is emailed.
type replyRequest struct {
	Message      string `json:"message"`
	InternalOnly bool   `json:"internalOnly"`
}

// cleanReplyText trims a typed message and returns the problem to show when it
// cannot be used ("" when it can).
func cleanReplyText(raw string, internal bool) (text, problem string) {
	text = strings.TrimSpace(raw)
	if text == "" {
		if internal {
			return "", "Write a note to save."
		}
		return "", "Write a message to send."
	}
	if utf8.RuneCountInString(text) > maxReplyChars {
		return "", "That message is too long."
	}
	return text, ""
}

// Reply emails the record's customer (or vendor) from the Communication tab and
// records it in the conversation. The reply carries the record's own Reply-To and
// the thread headers, so the answer comes back here and stays in the same Gmail
// or Outlook conversation. With internalOnly set it only saves a team note on the
// record: nothing is sent, and a notes-only record type (a requisition) accepts
// nothing else. Files may ride along, as multipart/form-data: they are copied into
// the tenant's bucket first, sent with the email, and recorded with the message,
// or removed again if the message is not recorded. RBAC: <type>:update.
func (h *RecordEmailOps) Reply(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, info, identityID, ok := authRecordAccess(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	kind, ok := threadKinds[info.WorkflowKey]
	if !ok {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}

	in, status, problem := readReply(w, r)
	if status != 0 {
		fail(w, status, problem)
		return
	}
	req := in.req
	text, problem := cleanReplyText(req.Message, req.InternalOnly)
	if problem != "" {
		fail(w, http.StatusBadRequest, problem)
		return
	}
	if err := recordemail.ValidateReplyFiles(in.files); err != nil {
		fail(w, http.StatusBadRequest, replyFilesProblem(err))
		return
	}
	ctx := r.Context()
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	// The message's id is chosen up front when it has files, so they can point at it.
	var messageID string
	if len(in.files) > 0 {
		if messageID, err = newAttachUUID(); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to prepare the message.")
			return
		}
	}

	// A note is never sent, so it needs neither inbound email nor a customer address.
	if req.InternalOnly {
		atts, discard, status, problem := h.keepReplyFiles(ctx, pool, tenant, info.WorkflowKey, recordID, messageID, in.files)
		if status != 0 {
			fail(w, status, problem)
			return
		}
		if !saveInternalNote(w, r, pool, info.WorkflowKey, recordID, identityID, text, messageID, atts) {
			discard()
		}
		return
	}
	if kind.notesOnly {
		fail(w, http.StatusBadRequest, notesOnlyRefusal)
		return
	}

	if !h.Enabled() {
		fail(w, http.StatusServiceUnavailable, "Email replies are not set up for this workspace.")
		return
	}

	to, err := recordemail.ReplyTarget(ctx, pool, recordID)
	if err != nil {
		slog.ErrorContext(ctx, "record email: reply target failed", "record", recordID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to prepare the reply.")
		return
	}
	if to == "" {
		fail(w, http.StatusConflict, noReplyTargetMessage(kind))
		return
	}
	replyTo, ok := h.ReplyAddress(ctx, tenant.ID, recordID, info.WorkflowKey)
	if !ok {
		fail(w, http.StatusServiceUnavailable, "Email replies are not set up for this workspace.")
		return
	}

	subject, inReplyTo, references := threadHeaders(ctx, pool, recordID, kind.fallbackSubject)
	actor := loadActor(ctx, pool, identityID)

	atts, discard, status, problem := h.keepReplyFiles(ctx, pool, tenant, info.WorkflowKey, recordID, messageID, in.files)
	if status != 0 {
		fail(w, status, problem)
		return
	}

	res, sendErr := h.notify(ctx, services.NotificationRequest{
		TenantID:        tenant.ID,
		Recipients:      []services.RecipientTarget{{Email: to}},
		ActorUserID:     identityID,
		EventType:       eventRecordEmailReply,
		Resource:        info.WorkflowKey,
		ResourceID:      recordID,
		StatusLink:      recordLink(info.WorkflowKey, recordID),
		Title:           subject,
		Body:            "Email reply sent.",
		Channels:        []string{"email"},
		PlainEmailHTML:  services.PlainTextEmailHTML(text),
		EmailReplyTo:    replyTo,
		EmailFromName:   senderLabel(actor.FullName, tenant.DisplayName),
		EmailInReplyTo:  inReplyTo,
		EmailReferences: references,
		Attachments:     notifyAttachments(in.files),
	})
	if sendErr != nil {
		discard()
		slog.WarnContext(ctx, "record email: reply not sent", "record", recordID, "error", sendErr)
		fail(w, http.StatusBadGateway, "Failed to send the email.")
		return
	}

	msg := recordemail.Message{
		ID: messageID, RecordID: recordID, WorkflowKey: info.WorkflowKey, Direction: recordemail.DirectionOut,
		FromName: actor.FullName, FromEmail: actor.Email, To: to, Subject: subject, Body: text,
		AttachmentCount: len(atts), SentByUserID: actor.ID,
	}
	if err := recordemail.InsertWithFiles(ctx, pool, msg, atts); err != nil {
		discard()
		slog.ErrorContext(ctx, "record email: reply sent but not recorded", "record", recordID, "error", err)
		fail(w, http.StatusInternalServerError, "The email was sent but could not be saved to the conversation.")
		return
	}
	_ = workflow.LogAudit(ctx, pool, actor.ID, "record_email.replied", "record_email", recordID,
		map[string]any{"recordId": recordID, "workflowKey": info.WorkflowKey, "to": to, "attachments": len(atts)})

	resp := map[string]any{"success": true}
	outcome := services.ConfirmEmailDelivery(ctx, tenant.ID, res, nil)
	applyEmailOutcome(ctx, resp, outcome, "record email: reply not delivered",
		"workflow", info.WorkflowKey, "record", recordID)
	writeJSON(w, http.StatusOK, resp)
}

// saveInternalNote files a team-only note on the record's conversation, with the
// files already kept for it (atts; messageID is the id they point at). Nothing is
// emailed, and the note is audit-logged like a reply. It reports whether the note
// was saved, so a caller that stored files can remove them when it was not.
func saveInternalNote(
	w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool,
	workflowKey, recordID, identityID, text, messageID string, atts []recordemail.Attachment,
) bool {
	ctx := r.Context()
	actor := loadActor(ctx, pool, identityID)
	note := recordemail.Message{
		ID: messageID, RecordID: recordID, WorkflowKey: workflowKey, Direction: recordemail.DirectionOut,
		FromName: actor.FullName, FromEmail: actor.Email, Body: text, AttachmentCount: len(atts),
		SentByUserID: actor.ID, Internal: true,
	}
	if err := recordemail.InsertWithFiles(ctx, pool, note, atts); err != nil {
		slog.ErrorContext(ctx, "record email: internal note not saved", "record", recordID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to save the note.")
		return false
	}
	_ = workflow.LogAudit(ctx, pool, actor.ID, "record_email.note_added", "record_email", recordID,
		map[string]any{"recordId": recordID, "workflowKey": workflowKey, "attachments": len(atts)})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
	return true
}

// notifyAttachments is the reply's files in the shape notify's create call takes.
func notifyAttachments(files []recordemail.ReplyFile) []services.NotifyAttachment {
	out := make([]services.NotifyAttachment, 0, len(files))
	for _, f := range files {
		out = append(out, services.NotifyAttachment{FileName: f.Name, ContentType: f.ContentType, Content: f.Data})
	}
	return out
}

// noReplyTargetMessage is what a reply attempt before the first email is told:
// the conversation starts with the record's own send button.
func noReplyTargetMessage(kind threadKind) string {
	return "There is no " + kind.party + " email to reply to yet. Use " + kind.sendAction + " first."
}

// threadHeaders returns the reply's subject and its In-Reply-To / References
// values: "Re:" the conversation's latest subject (fallbackSubject when there is
// none), answering the newest inbound message and quoting every inbound
// Message-ID so mail clients keep one thread.
func threadHeaders(ctx context.Context, pool *pgxpool.Pool, recordID, fallbackSubject string) (subject, inReplyTo, references string) {
	subject = recordemail.ReplySubject(fallbackSubject)
	if latest, err := recordemail.Latest(ctx, pool, recordID); err != nil {
		slog.WarnContext(ctx, "record email: latest message unavailable", "record", recordID, "error", err)
	} else if latest != nil {
		subject = recordemail.ReplySubject(latest.Subject)
	}
	ids, err := recordemail.InboundMessageIDs(ctx, pool, recordID)
	if err != nil {
		slog.WarnContext(ctx, "record email: thread references unavailable", "record", recordID, "error", err)
		return subject, "", ""
	}
	if len(ids) == 0 {
		return subject, "", ""
	}
	return subject, ids[len(ids)-1], strings.Join(ids, " ")
}

// loadActor returns the signed-in staff user's id/name/email, or a zero User
// (never an error) when it cannot be read: the mail still goes out, just
// without a person's name on it.
func loadActor(ctx context.Context, pool *pgxpool.Pool, identityID string) userstore.User {
	userID, err := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err != nil {
		return userstore.User{}
	}
	u, err := userstore.GetUserByID(ctx, pool, userID)
	if err != nil || u == nil {
		return userstore.User{}
	}
	return *u
}

// senderLabel is how the customer sees the sender: the rep's name with the
// company in brackets ("Harish (Acme Stone)"), or just the company when the
// rep's name is unknown.
func senderLabel(personName, company string) string {
	person, company := strings.TrimSpace(personName), strings.TrimSpace(company)
	switch {
	case person != "" && company != "":
		return person + " (" + company + ")"
	case person != "":
		return person
	default:
		return company
	}
}
