// approvalchain/notify.go
package approvalchain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// EventContext bundles the record identity and table/column names one of
// the Notify* hooks below needs to resolve recipients and build a
// notification. A caller builds one from data it already has in scope (a
// module's own Transition, or Approve/finalize) and sets only the fields the
// hook it's calling actually reads -- the rest are harmlessly ignored.
type EventContext struct {
	Table, IDColumn, NumberColumn, OwnerColumn string
	ApproverTable, ApprovalTable               string
	RecordTypeID, StatusID, InternalID         int
	ActorEmployeeID                            int
	Resource, DisplayName, RecordUUID          string
	// Detail is the approver's reason, read only by NotifyApprovalRejected.
	Detail string
}

// contact is one resolved notification recipient: a StoneSuite user reached
// via their employee record. UserID is the control-plane identity id
// (users.identity_id), NOT the tenant-local users.id -- stonesuite-notify
// scopes every bell/feed query by the JWT's "id" claim, which is the identity
// id (see stonesuite-notify middleware/auth.go and generateTenantJWT), so a
// recipient keyed by users.id would create a row the bell can never find.
type contact struct {
	UserID string
	Email  string
	Name   string // users.full_name, for the email greeting
}

func contactsToRecipients(contacts []contact) []services.RecipientTarget {
	recipients := make([]services.RecipientTarget, len(contacts))
	for i, c := range contacts {
		recipients[i] = services.RecipientTarget{UserID: c.UserID, Email: c.Email, Name: c.Name}
	}
	return recipients
}

// NotifyApprovalRequested best-effort-notifies every active approver
// configured on ec.ApproverTable at (ec.RecordTypeID, ec.StatusID) that a
// record now needs their sign-off. No-ops immediately if ec.DisplayName ==
// "" -- the guard that keeps this a no-op for every module not yet
// configured for approval notifications (see ModuleConfig.DisplayName).
// Never returns an error: sending a notification is not part of the
// caller's transaction contract, so every failure is logged and swallowed,
// matching documents.go's notifyOwnerOfSend precedent.
func NotifyApprovalRequested(ctx context.Context, pool *pgxpool.Pool, ec EventContext) {
	if ec.DisplayName == "" {
		return
	}
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "approvalchain: no tenant in context, skipping approval-requested notification", "resource", ec.Resource, "recordId", ec.RecordUUID)
		return
	}
	contacts, err := activeApproverContacts(ctx, pool, ec.ApproverTable, ec.RecordTypeID, ec.StatusID)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve approvers for approval-requested notification failed", "resource", ec.Resource, "recordId", ec.RecordUUID, "error", err)
		return
	}
	if len(contacts) == 0 {
		return
	}
	facts, err := fetchFacts(ctx, pool, ec)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve record number for approval-requested notification failed", "resource", ec.Resource, "recordId", ec.RecordUUID, "error", err)
		return
	}
	sendApprovalNotification(ctx, tenant.ID, ec.Resource+".approval_requested", facts,
		resolveActorUserID(ctx, pool, ec.ActorEmployeeID), ec, noteApprovalRequested, contacts)
}

// NotifyRemainingApprovers best-effort-notifies the approvers who have not
// yet signed off on a still-gated record, after one more sign-off was just
// recorded (quorum not yet met). Same no-op/failure semantics as
// NotifyApprovalRequested.
func NotifyRemainingApprovers(ctx context.Context, pool *pgxpool.Pool, ec EventContext) {
	if ec.DisplayName == "" {
		return
	}
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "approvalchain: no tenant in context, skipping approval reminder", "resource", ec.Resource, "recordId", ec.RecordUUID)
		return
	}
	contacts, err := remainingApproverContacts(ctx, pool, ec.ApproverTable, ec.ApprovalTable, ec.IDColumn, ec.RecordTypeID, ec.StatusID, ec.InternalID)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve remaining approvers failed", "resource", ec.Resource, "recordId", ec.RecordUUID, "error", err)
		return
	}
	if len(contacts) == 0 {
		return
	}
	facts, err := fetchFacts(ctx, pool, ec)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve record number for approval reminder failed", "resource", ec.Resource, "recordId", ec.RecordUUID, "error", err)
		return
	}
	sendApprovalNotification(ctx, tenant.ID, ec.Resource+".approval_requested", facts,
		resolveActorUserID(ctx, pool, ec.ActorEmployeeID), ec, noteApprovalReminder, contacts)
}

// NotifyApproved best-effort-notifies a record's owner that it has been
// approved (quorum met or a super-admin override). Same no-op/failure
// semantics as NotifyApprovalRequested.
func NotifyApproved(ctx context.Context, pool *pgxpool.Pool, ec EventContext) {
	notifyOwner(ctx, pool, ec, ec.Resource+".approved", noteApproved)
}

// NotifyApprovalRejected best-effort-notifies a record's owner that it left
// a gated, unapproved status via an escape route (void/cancel/reject)
// instead of clearing approval. Same no-op/failure semantics as
// NotifyApprovalRequested.
func NotifyApprovalRejected(ctx context.Context, pool *pgxpool.Pool, ec EventContext) {
	note := noteSentBack
	note.Body = rejectedNotificationBody(ec.Detail)
	notifyOwner(ctx, pool, ec, ec.Resource+".approval_rejected", note)
}

// rejectedNotificationBody words the "sent back" notification: it says why
// when an approver gave a reason (Reject), and stays generic for the escapes
// that carry none (a plain void/cancel out of a gate).
func rejectedNotificationBody(detail string) string {
	if detail == "" {
		return noteSentBack.Body
	}
	return "Sent back for changes: " + detail
}

// NotifyCreated best-effort-notifies the actor who created a new record
// (not necessarily its owner -- owner and actor can diverge via an
// explicit OwnerEmployeeID override on create) that the creation
// succeeded. Same no-op/failure semantics as NotifyApprovalRequested.
func NotifyCreated(ctx context.Context, pool *pgxpool.Pool, ec EventContext) {
	if ec.DisplayName == "" {
		return
	}
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "approvalchain: no tenant in context, skipping created notification", "resource", ec.Resource, "recordId", ec.RecordUUID)
		return
	}
	actor, ok, err := actorContact(ctx, pool, ec.ActorEmployeeID)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve actor for created notification failed", "resource", ec.Resource, "recordId", ec.RecordUUID, "error", err)
		return
	}
	if !ok {
		return
	}
	facts, err := fetchFacts(ctx, pool, ec)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve record number for created notification failed", "resource", ec.Resource, "recordId", ec.RecordUUID, "error", err)
		return
	}
	sendApprovalNotification(ctx, tenant.ID, ec.Resource+".created", facts,
		actor.UserID, ec, noteCreated, []contact{actor})
}

// ActorCreated describes a record that has no approval workflow (a CRM task,
// an inventory item) whose creator gets a plain confirmation email. Unlike
// EventContext it carries no table/number facts: the title and body are
// supplied by the caller.
type ActorCreated struct {
	EventType       string // e.g. "crm_activity.created"
	Resource        string // RBAC resource the bell filter uses, e.g. "crm_activity"
	RecordUUID      string
	ActorEmployeeID int
	Title, Body     string
}

// NotifyActorCreated best-effort-emails the employee who created a record
// that has no approval chain. Same failure contract as NotifyCreated: every
// failure is logged and swallowed, so the create that already committed is
// never undone by a notify outage.
func NotifyActorCreated(ctx context.Context, pool *pgxpool.Pool, ev ActorCreated) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "approvalchain: no tenant in context, skipping created notification", "eventType", ev.EventType, "recordId", ev.RecordUUID)
		return
	}
	actor, ok, err := actorContact(ctx, pool, ev.ActorEmployeeID)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve actor for created notification failed", "eventType", ev.EventType, "recordId", ev.RecordUUID, "error", err)
		return
	}
	if !ok {
		return
	}
	sendActorCreated(ctx, tenant.ID, actor, actor.UserID, ev)
}

// NotifyOwnerCreated best-effort-emails the owner of a record (the employee in
// ownerColumn) that the record was created without them as the actor -- e.g. a
// customer's portal order-form submission. A record with no owner is logged
// and skipped rather than guessing a recipient. Same failure contract as
// NotifyActorCreated.
func NotifyOwnerCreated(ctx context.Context, pool *pgxpool.Pool, table, idColumn, ownerColumn string, internalID int, ev ActorCreated) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "approvalchain: no tenant in context, skipping owner created notification", "eventType", ev.EventType, "recordId", ev.RecordUUID)
		return
	}
	owner, ok, err := ownerContact(ctx, pool, table, idColumn, ownerColumn, internalID)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve owner for created notification failed", "eventType", ev.EventType, "recordId", ev.RecordUUID, "error", err)
		return
	}
	if !ok {
		slog.WarnContext(ctx, "approvalchain: record has no owner, created notification skipped", "eventType", ev.EventType, "recordId", ev.RecordUUID)
		return
	}
	sendActorCreated(ctx, tenant.ID, owner, "", ev)
}

// sendActorCreated sends one created-event email to recipient. actorUserID is
// who performed the create ("" when no staff member did, e.g. a portal submit).
func sendActorCreated(ctx context.Context, tenantID string, recipient contact, actorUserID string, ev ActorCreated) {
	err := services.SendNotification(ctx, buildActorCreatedRequest(tenantID, recipient, actorUserID, ev))
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: send created notification failed", "eventType", ev.EventType, "recordId", ev.RecordUUID, "error", err)
	}
}

// buildActorCreatedRequest is the Notify request for a created-event email:
// one email-channel recipient, the record as the resource, and no approval
// facts. Pure, so the wire shape is testable without Notify.
func buildActorCreatedRequest(tenantID string, recipient contact, actorUserID string, ev ActorCreated) services.NotificationRequest {
	return services.NotificationRequest{
		TenantID:    tenantID,
		Recipients:  contactsToRecipients([]contact{recipient}),
		ActorUserID: actorUserID,
		EventType:   ev.EventType,
		Resource:    ev.Resource,
		ResourceID:  ev.RecordUUID,
		Title:       ev.Title,
		Body:        ev.Body,
		Channels:    []string{"email"},
	}
}

func notifyOwner(ctx context.Context, pool *pgxpool.Pool, ec EventContext, eventType string, note approvalNote) {
	if ec.DisplayName == "" {
		return
	}
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "approvalchain: no tenant in context, skipping owner notification", "eventType", eventType, "recordId", ec.RecordUUID)
		return
	}
	owner, ok, err := ownerContact(ctx, pool, ec.Table, ec.IDColumn, ec.OwnerColumn, ec.InternalID)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve owner for notification failed", "eventType", eventType, "recordId", ec.RecordUUID, "error", err)
		return
	}
	if !ok {
		return
	}
	facts, err := fetchFacts(ctx, pool, ec)
	if err != nil {
		slog.ErrorContext(ctx, "approvalchain: resolve record number for owner notification failed", "eventType", eventType, "recordId", ec.RecordUUID, "error", err)
		return
	}
	sendApprovalNotification(ctx, tenant.ID, eventType, facts,
		resolveActorUserID(ctx, pool, ec.ActorEmployeeID), ec, note, []contact{owner})
}

// activeApproverContacts resolves every active approver configured on
// approverTable at (recordTypeID, statusID) into notifiable contacts.
// Mirrors GetInfo's own approver join, but selects u.identity_id (the id
// stonesuite-notify scopes by, see the contact type) plus u.email.
func activeApproverContacts(ctx context.Context, q workflow.Querier, approverTable string, recordTypeID, statusID int) ([]contact, error) {
	rows, err := q.Query(ctx, fmt.Sprintf(`
		SELECT u.identity_id, u.email, u.full_name
		FROM %s ea
		JOIN employee e ON e.employee_id = ea.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE ea.record_type_id = $1 AND ea.record_status_id = $2 AND ea.is_active`,
		approverTable), recordTypeID, statusID)
	if err != nil {
		return nil, fmt.Errorf("load active approvers from %s: %w", approverTable, err)
	}
	defer rows.Close()
	return scanContacts(rows)
}

// remainingApproverContacts resolves active approvers at (recordTypeID,
// statusID) who have not yet signed off on internalID's current round.
func remainingApproverContacts(ctx context.Context, q workflow.Querier, approverTable, approvalTable, idColumn string, recordTypeID, statusID, internalID int) ([]contact, error) {
	rows, err := q.Query(ctx, fmt.Sprintf(`
		SELECT u.identity_id, u.email, u.full_name
		FROM %s ea
		JOIN employee e ON e.employee_id = ea.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE ea.record_type_id = $1 AND ea.record_status_id = $2 AND ea.is_active
			AND NOT EXISTS (
				SELECT 1 FROM %s ap
				WHERE ap.%s = $3 AND ap.record_status_id = $2 AND ap.approver_employee_id = ea.approver_employee_id
			)`, approverTable, approvalTable, idColumn),
		recordTypeID, statusID, internalID)
	if err != nil {
		return nil, fmt.Errorf("load remaining approvers from %s: %w", approverTable, err)
	}
	defer rows.Close()
	return scanContacts(rows)
}

func scanContacts(rows pgx.Rows) ([]contact, error) {
	var contacts []contact
	for rows.Next() {
		var c contact
		if err := rows.Scan(&c.UserID, &c.Email, &c.Name); err != nil {
			return nil, fmt.Errorf("scan approver contact: %w", err)
		}
		contacts = append(contacts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approver contacts: %w", err)
	}
	return contacts, nil
}

// ownerContact resolves table's owner (via ownerColumn, an employee FK) into
// a notifiable contact. ok is false when the record row or its owner
// doesn't resolve (e.g. OwnerColumn is NULL).
func ownerContact(ctx context.Context, q workflow.Querier, table, idColumn, ownerColumn string, internalID int) (contact, bool, error) {
	var c contact
	err := q.QueryRow(ctx, fmt.Sprintf(`
		SELECT u.identity_id, u.email, u.full_name
		FROM %s t
		JOIN employee e ON e.employee_id = t.%s
		JOIN users u ON u.id = e.employee_user_id
		WHERE t.%s = $1`, table, ownerColumn, idColumn), internalID).Scan(&c.UserID, &c.Email, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return contact{}, false, nil
	}
	if err != nil {
		return contact{}, false, fmt.Errorf("resolve owner contact on %s: %w", table, err)
	}
	return c, true, nil
}

// actorContact resolves actorEmployeeID's linked user into a notifiable
// contact. ok is false when actorEmployeeID is 0 or unresolvable (e.g. no
// linked user) -- mirrors ownerContact's shape, but unlike
// resolveActorUserID (which only returns a bare UserID for ActorUserID
// metadata) this also returns email, since the actor is a Recipient here.
func actorContact(ctx context.Context, q workflow.Querier, actorEmployeeID int) (contact, bool, error) {
	if actorEmployeeID == 0 {
		return contact{}, false, nil
	}
	var c contact
	err := q.QueryRow(ctx, `
		SELECT u.identity_id, u.email, u.full_name
		FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE e.employee_id = $1`, actorEmployeeID).Scan(&c.UserID, &c.Email, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.WarnContext(ctx, "approvalchain: actor has no linked user, created notification skipped", "employeeId", actorEmployeeID)
		return contact{}, false, nil
	}
	if err != nil {
		return contact{}, false, fmt.Errorf("resolve actor contact: %w", err)
	}
	return c, true, nil
}

// fetchNumber reads a record's display number (e.g. "INV-000123"), used in
// notification title text.
func fetchNumber(ctx context.Context, q workflow.Querier, table, idColumn, numberColumn string, internalID int) (string, error) {
	var number string
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1`, numberColumn, table, idColumn), internalID).Scan(&number); err != nil {
		return "", fmt.Errorf("resolve %s number: %w", table, err)
	}
	return number, nil
}

// resolveActorUserID best-effort-resolves an approval actor's employee id to
// their control-plane identity id (the value stonesuite-notify stores as a
// notification's actor_user_id -- consistent with recipient ids, see the
// contact type) -- "" if unresolvable (e.g. actorEmployeeID is 0, or has no
// linked user), which just omits ActorUserID from the notification rather
// than failing it.
func resolveActorUserID(ctx context.Context, q workflow.Querier, actorEmployeeID int) string {
	if actorEmployeeID == 0 {
		return ""
	}
	var identityID string
	if err := q.QueryRow(ctx, `
		SELECT u.identity_id
		FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE e.employee_id = $1`, actorEmployeeID).Scan(&identityID); err != nil {
		return ""
	}
	return identityID
}
