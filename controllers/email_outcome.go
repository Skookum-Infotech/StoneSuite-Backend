package controllers

import (
	"context"
	"log/slog"

	"stonesuite-backend/services"
)

// applyEmailOutcome adds the email result to a JSON response the way the UI
// reads it: emailSent always, plus a client-safe emailError when the mail was
// not sent. It does nothing when no email was attempted, so a response never
// claims a delivery that did not happen. The provider detail stays in the log.
func applyEmailOutcome(ctx context.Context, resp map[string]any, o services.EmailOutcome, logMsg string, logAttrs ...any) {
	if !o.Attempted() {
		return
	}
	resp["emailSent"] = o.Sent()
	if o.Sent() {
		return
	}
	resp["emailError"] = o.UserMessage()
	attrs := append([]any{"email_status", string(o.Status), "detail", o.Detail}, logAttrs...)
	slog.WarnContext(ctx, logMsg, attrs...)
}
