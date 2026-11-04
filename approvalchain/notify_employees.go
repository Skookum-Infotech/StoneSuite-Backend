package approvalchain

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// EmployeesEvent is a notification addressed to a set of employees (not an approval step).
// Employees without a linked, active user are skipped. Unlike the approval hooks it does not
// depend on a registry DisplayName: the caller supplies the title and body.
type EmployeesEvent struct {
	EventType      string // e.g. "install_appointment.scheduled"
	Resource       string // RBAC resource the bell filter uses, e.g. "install_appointment"
	RecordUUID     string
	ActorUserID    string // control-plane identity id of whoever made the change
	Title, Body    string
	StatusLink     string
	EmployeeIDs    []int
	IdempotencyKey string
}

// notifyChannelEmail is the only channel used for employee notifications.
const notifyChannelEmail = "email"

// NotifyEmployees sends ev to the users linked to ev.EmployeeIDs and returns the notify ids.
// Employees with no login are skipped without error; no resolvable recipient is a no-op.
func NotifyEmployees(ctx context.Context, pool *pgxpool.Pool, ev EmployeesEvent) (services.NotificationResult, error) {
	tenant, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		return services.NotificationResult{}, fmt.Errorf("notify employees: %w", err)
	}
	return notifyEmployees(ctx, pool, tenant.ID, ev)
}

// notifyEmployees is NotifyEmployees with the tenant id already resolved.
func notifyEmployees(ctx context.Context, q workflow.Querier, tenantID string, ev EmployeesEvent) (services.NotificationResult, error) {
	contacts, err := resolveEmployeeContacts(ctx, q, ev.EmployeeIDs)
	if err != nil {
		return services.NotificationResult{}, err
	}
	if len(contacts) == 0 {
		return services.NotificationResult{}, nil
	}
	res, err := services.SendNotificationWithResult(ctx, buildEmployeesRequest(tenantID, contacts, ev))
	if err != nil {
		return res, fmt.Errorf("send employee notification %s: %w", ev.EventType, err)
	}
	return res, nil
}

// buildEmployeesRequest is the Notify request for an employee notification. Pure.
func buildEmployeesRequest(tenantID string, contacts []contact, ev EmployeesEvent) services.NotificationRequest {
	return services.NotificationRequest{
		TenantID:       tenantID,
		Recipients:     contactsToRecipients(contacts),
		ActorUserID:    ev.ActorUserID,
		EventType:      ev.EventType,
		Resource:       ev.Resource,
		ResourceID:     ev.RecordUUID,
		Title:          ev.Title,
		Body:           ev.Body,
		StatusLink:     ev.StatusLink,
		Channels:       []string{notifyChannelEmail},
		IdempotencyKey: ev.IdempotencyKey,
	}
}

// resolveEmployeeContacts maps employee ids to their active users; employees with no login are logged and skipped.
func resolveEmployeeContacts(ctx context.Context, q workflow.Querier, employeeIDs []int) ([]contact, error) {
	if len(employeeIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT e.employee_id, u.identity_id, u.email, u.full_name
		FROM employee e
		JOIN users u ON u.id = e.employee_user_id AND u.status = 'active'
		WHERE e.employee_id = ANY($1) AND e.employee_deleted_at IS NULL
		ORDER BY e.employee_id`, employeeIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve employee contacts: %w", err)
	}
	defer rows.Close()
	found := make(map[int]bool, len(employeeIDs))
	var contacts []contact
	for rows.Next() {
		var id int
		var c contact
		if err := rows.Scan(&id, &c.UserID, &c.Email, &c.Name); err != nil {
			return nil, fmt.Errorf("scan employee contact: %w", err)
		}
		found[id] = true
		contacts = append(contacts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate employee contacts: %w", err)
	}
	for _, id := range employeeIDs {
		if !found[id] {
			slog.InfoContext(ctx, "install notify skipped: no login", "employee_id", id)
		}
	}
	return contacts, nil
}
