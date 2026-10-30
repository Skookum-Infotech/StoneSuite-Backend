package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

const (
	// sendNotifyKeyPrefix namespaces the Notify idempotency key of a customer send.
	sendNotifyKeyPrefix = "document-send:"
	// ownerPingKeyPrefix namespaces the Notify idempotency key of the owner ping.
	ownerPingKeyPrefix = "document-send-owner-ping:"
	// sendFieldSep separates scalar fields in a send fingerprint; the unit
	// separator cannot appear in a validated address or header-safe subject.
	sendFieldSep = "\x1f"
	// sendListSep separates the addresses within one list field.
	sendListSep = "\x1e"

	// sendPendingLease is how long a pending attempt is treated as in flight.
	// Notify calls are sequential per recipient with a 30s client timeout each,
	// so the lease must clear several of them before a retry may reclaim it.
	sendPendingLease = 5 * time.Minute

	// securityEventSendKeyReused is logged when a key is replayed for a different send.
	securityEventSendKeyReused = "document_send_key_reused"

	msgIdemKeyReused  = "Idempotency-Key was already used for a different send."
	msgSendInProgress = "A send with this Idempotency-Key is still in progress."
)

// sendNotifyFunc queues one Notify request and returns the created notification ids.
type sendNotifyFunc func(context.Context, services.NotificationRequest) (services.NotificationResult, error)

// notifySender returns the injected notify call, defaulting to the real client.
func (h *DocumentOps) notifySender() sendNotifyFunc {
	if h.sendNotify != nil {
		return h.sendNotify
	}
	return services.SendNotificationWithResult
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// documentSendNotifyKey is the Notify idempotency key for the customer emails
// of one send attempt. Notify scopes keys per recipient, so one key per send
// dedupes each recipient individually on retry.
func documentSendNotifyKey(sendID string) string { return sha256Hex(sendNotifyKeyPrefix + sendID) }

// documentOwnerPingKey is the Notify idempotency key for the owner ping of a send.
func documentOwnerPingKey(sendID string) string { return sha256Hex(ownerPingKeyPrefix + sendID) }

// sendFingerprint hashes what makes two sends "the same request": workflow,
// recipients, subject and message. The record is already part of the attempt key.
func sendFingerprint(p *preparedSend) string {
	return sha256Hex(strings.Join([]string{
		p.meta.WorkflowKey,
		strings.Join(p.to, sendListSep),
		strings.Join(p.cc, sendListSep),
		p.subject,
		p.message,
	}, sendFieldSep))
}

// sendAttemptOutcome is what runSendAttempt reports to deliver.
type sendAttemptOutcome struct {
	SendID string
	Notify services.NotificationResult
	// Replay is true when the send had already completed under this key; no
	// email was queued and nothing else should be re-recorded.
	Replay bool
}

// runSendAttempt drives one pending-first send: create-or-get the attempt,
// claim it, queue the email with an attempt-derived idempotency key, then
// finalize into document_sends. buildReq is called with the attempt id.
func runSendAttempt(
	ctx context.Context, db workflow.Beginner, notify sendNotifyFunc,
	attempt workflow.DocumentSendAttempt, record workflow.DocumentSend,
	buildReq func(attemptID string) services.NotificationRequest,
) (sendAttemptOutcome, *sendError) {
	// The sender check below is what stops one user replaying another's send,
	// so it must never compare two empty ids: without a tenant user the key is
	// dropped and the send runs as a fresh attempt (fail closed, never replay).
	if attempt.IdempotencyKey != "" && attempt.SentByUserID == "" {
		slog.WarnContext(ctx, "document send: idempotency key ignored, no tenant user",
			"workflow", attempt.WorkflowKey, "record", attempt.RecordID)
		attempt.IdempotencyKey = ""
	}
	existing, created, err := workflow.CreateOrGetDocumentSendAttempt(ctx, db, attempt)
	if err != nil {
		slog.ErrorContext(ctx, "document send: create attempt failed",
			"workflow", attempt.WorkflowKey, "record", attempt.RecordID, "error", err)
		return sendAttemptOutcome{}, &sendError{Status: http.StatusInternalServerError, Msg: "Failed to record send."}
	}
	if !created {
		if existing.RequestFingerprint != attempt.RequestFingerprint || existing.SentByUserID != attempt.SentByUserID {
			slog.WarnContext(ctx, "security event", "security_event", securityEventSendKeyReused,
				"record", attempt.RecordID, "workflow", attempt.WorkflowKey, "attempt", existing.ID,
				"sent_by", attempt.SentByUserID, "original_sent_by", existing.SentByUserID)
			return sendAttemptOutcome{}, &sendError{Status: http.StatusConflict, Msg: msgIdemKeyReused}
		}
		if existing.Status == workflow.SendAttemptSent {
			return sendAttemptOutcome{
				SendID: existing.ID, Replay: true,
				Notify: services.NotificationResult{NotificationIDs: existing.NotifyNotificationIDs},
			}, nil
		}
		claimed, err := workflow.ClaimDocumentSendAttempt(ctx, db, existing.ID, sendPendingLease)
		if err != nil {
			slog.ErrorContext(ctx, "document send: claim attempt failed", "attempt", existing.ID, "error", err)
			return sendAttemptOutcome{}, &sendError{Status: http.StatusInternalServerError, Msg: "Failed to record send."}
		}
		if !claimed {
			return sendAttemptOutcome{}, &sendError{Status: http.StatusConflict, Msg: msgSendInProgress}
		}
	}

	req := buildReq(existing.ID)
	req.IdempotencyKey = documentSendNotifyKey(existing.ID)
	res, err := notify(ctx, req)
	if err != nil {
		slog.WarnContext(ctx, "document send: notify failed",
			"workflow", attempt.WorkflowKey, "record", attempt.RecordID, "error", err)
		markAttemptFailed(ctx, db, existing.ID)
		return sendAttemptOutcome{}, &sendError{Status: http.StatusBadGateway, Msg: "Failed to send email."}
	}

	record.NotifyNotificationIDs = res.NotificationIDs
	if err := workflow.FinalizeDocumentSend(ctx, db, existing.ID, record); err != nil {
		slog.ErrorContext(ctx, "document send: email queued but send not recorded",
			"send_id", existing.ID, "record", attempt.RecordID, "workflow", attempt.WorkflowKey,
			"notification_ids", res.NotificationIDs, "error", err)
		markAttemptFailed(ctx, db, existing.ID)
		return sendAttemptOutcome{}, &sendError{Status: http.StatusInternalServerError, Msg: "Failed to record send."}
	}
	return sendAttemptOutcome{SendID: existing.ID, Notify: res}, nil
}

// markAttemptFailed best-effort flags the attempt failed, logging if that fails too.
func markAttemptFailed(ctx context.Context, db workflow.Querier, attemptID string) {
	if err := workflow.MarkDocumentSendAttemptFailed(ctx, db, attemptID); err != nil {
		slog.ErrorContext(ctx, "document send: mark attempt failed", "attempt", attemptID, "error", err)
	}
}
