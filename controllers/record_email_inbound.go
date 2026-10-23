package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/middleware"
	"stonesuite-backend/recordemail"
	"stonesuite-backend/tenancy"
)

const (
	// InboundEmailPath is the machine-only route stonesuite-notify forwards a
	// customer's reply to, after receiving it from Resend and fetching its content.
	InboundEmailPath = "/api/internal/email/inbound"

	// maxInboundBodyBytes caps the forwarded mail. notify sends at most 256 KiB of
	// text and 256 KiB of html plus a few small headers, so 1 MiB is generous.
	maxInboundBodyBytes = 1 << 20
)

// WithInbound enables the inbound-reply route and returns h. secret is the key
// stonesuite-notify signs the request with (NOTIFY_API_KEY, notify's
// INTERNAL_SERVICE_SECRET); tenants and poolFor resolve the tenant database a
// reply belongs to (the same pair DocumentOps.WithPublicDownload takes).
func (h *RecordEmailOps) WithInbound(
	secret string, tenants docLinkTenants,
	poolFor func(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error),
) *RecordEmailOps {
	h.secret, h.tenants, h.poolFor = secret, tenants, poolFor
	h.replays = middleware.NewReplayGuard()
	return h
}

// Inbound receives a customer's reply from stonesuite-notify and files it under
// the record its address names. There is no login: the HMAC signature over the
// method, path, timestamp and body is the only authentication, and it is checked
// before anything is read or written. This service never talks to Resend; notify
// receives Resend's webhook and fetches the mail.
//
// Status codes drive notify's retries. A mail that is not ours to file (no reply
// address in its recipients, an unknown token, a suspended tenant) is
// acknowledged with 200 so it is not retried forever; a failure that retrying can
// fix (our database) answers 5xx.
//
// The files that came with the mail are only recorded here. The answer lists the
// ones still to be copied (`pending`, the provider's ids) and the `uploadRef` to
// send them under, and notify then sends each one to InboundAttachment. Mail
// from an older notify, which sends only a count, has no files to copy.
func (h *RecordEmailOps) Inbound(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() || h.secret == "" || h.tenants == nil || h.poolFor == nil {
		fail(w, http.StatusServiceUnavailable, "Inbound email is not configured.")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBodyBytes))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "Request body too large.")
		return
	}
	sig := r.Header.Get(middleware.InternalSignatureHeader)
	if err := middleware.VerifyInternalSignature(h.secret, r.Method, r.URL.Path,
		r.Header.Get(middleware.InternalTimestampHeader), sig, body, h.now()); err != nil {
		logSecurityEvent(r, "inbound_email_bad_signature", "reason", err.Error())
		fail(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	if !h.replays.FirstUse(sig, h.now()) {
		logSecurityEvent(r, "inbound_email_replay")
		fail(w, http.StatusUnauthorized, "Unauthorized.")
		return
	}

	var mailIn recordemail.InboundMail
	if err := json.Unmarshal(body, &mailIn); err != nil || mailIn.EmailID == "" {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	filed, status, msg := h.fileInbound(r.Context(), mailIn)
	if status != http.StatusOK {
		fail(w, status, msg)
		return
	}
	pending := filed.pending
	if pending == nil {
		pending = []string{} // an array in the answer, never null
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "uploadRef": filed.ref, "pending": pending})
}

// inboundFiled is what filing a mail leaves for notify to do: copy the files in
// pending, each under ref. Both are empty when there is nothing to copy.
type inboundFiled struct {
	ref     string
	pending []string
}

// fileInbound resolves the reply address and stores the mail and the metadata of
// its files. It returns http.StatusOK both when the message was filed and when it
// is not ours to file (see Inbound), and a 5xx when a retry could succeed.
func (h *RecordEmailOps) fileInbound(ctx context.Context, mailIn recordemail.InboundMail) (inboundFiled, int, string) {
	token, ok := recordemail.TokenFromAddresses(mailIn.Recipients(), h.domain)
	if !ok {
		slog.InfoContext(ctx, "inbound email: no reply address among recipients; ignored", "email", mailIn.EmailID)
		return inboundFiled{}, http.StatusOK, ""
	}
	target, err := h.addrs.Resolve(ctx, token)
	if errors.Is(err, recordemail.ErrAddressNotFound) {
		slog.InfoContext(ctx, "inbound email: unknown reply address; ignored", "email", mailIn.EmailID)
		return inboundFiled{}, http.StatusOK, ""
	}
	if err != nil {
		slog.ErrorContext(ctx, "inbound email: resolve address failed", "email", mailIn.EmailID, "error", err)
		return inboundFiled{}, http.StatusInternalServerError, "Failed to resolve the reply address."
	}

	tenant, err := h.tenants.TenantByID(ctx, target.TenantID)
	if err != nil || tenant.Status != tenancy.StatusActive {
		slog.InfoContext(ctx, "inbound email: tenant unavailable; ignored", "email", mailIn.EmailID, "tenant", target.TenantID)
		return inboundFiled{}, http.StatusOK, ""
	}
	pool, err := h.poolFor(ctx, tenant)
	if err != nil {
		slog.ErrorContext(ctx, "inbound email: tenant pool unavailable", "tenant", target.TenantID, "error", err)
		return inboundFiled{}, http.StatusInternalServerError, "Failed to open the workspace."
	}

	inserted, err := recordemail.Insert(ctx, pool, recordemail.BuildMessage(target, mailIn))
	if err != nil {
		slog.ErrorContext(ctx, "inbound email: store failed", "email", mailIn.EmailID, "record", target.RecordID, "error", err)
		return inboundFiled{}, http.StatusInternalServerError, "Failed to store the email."
	}
	slog.InfoContext(ctx, "inbound email filed", "email", mailIn.EmailID, "record", target.RecordID, "new", inserted)

	// Record the files too, even when the message was already there: a retried
	// delivery returns the files an earlier attempt left pending.
	pending, err := recordemail.FileReceived(ctx, pool, target.RecordID, mailIn.EmailID,
		mailIn.ReceivedFiles(), h.storeFor(tenant) != nil)
	if err != nil {
		slog.ErrorContext(ctx, "inbound email: attachments not recorded", "email", mailIn.EmailID, "record", target.RecordID, "error", err)
		return inboundFiled{}, http.StatusInternalServerError, "Failed to record the attachments."
	}
	filed := inboundFiled{pending: pending}
	if len(pending) > 0 {
		filed.ref = token
	}
	return filed, http.StatusOK, ""
}
