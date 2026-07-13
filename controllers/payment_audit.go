package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/payment"
	"stonesuite-backend/workflow"
)

// paymentSnapshot flattens a Payment into the map recorded in audit_logs.
func paymentSnapshot(p *payment.Payment) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":              p.ID,
		"number":          p.Number,
		"statusCode":      p.StatusCode,
		"customerId":      p.Customer.ID,
		"ownerUserId":     p.OwnerUserID,
		"amount":          p.Amount,
		"appliedTotal":    p.AppliedTotal,
		"unappliedAmount": p.UnappliedAmount,
		"customFields":    p.CustomFields,
	}
}

// auditPayment records a create/update/delete/transition event for a payment.
func auditPayment(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, paymentID string, oldPayment, newPayment *payment.Payment) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourcePayment), paymentID, "payment",
		paymentSnapshot(oldPayment), paymentSnapshot(newPayment), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("payment: audit %s %s: %v", action, paymentID, err)
	}
}
