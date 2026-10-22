package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AutomationDelivery is one delivery-log row as exposed by the deliveries API.
type AutomationDelivery struct {
	ID             string     `json:"id"`
	RuleKey        string     `json:"ruleKey"`
	Resource       string     `json:"resource"`
	RecordID       string     `json:"recordId"`
	Stage          int        `json:"stage"`
	AnchorDate     string     `json:"anchorDate"`
	RecipientEmail string     `json:"recipientEmail"`
	Status         string     `json:"status"`
	Reason         *string    `json:"reason"`
	Attempts       int        `json:"attempts"`
	RunAt          time.Time  `json:"runAt"`
	SentAt         *time.Time `json:"sentAt"`
	LastError      *string    `json:"lastError"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// DeliveryFilter is the validated query for one page of the delivery log.
type DeliveryFilter struct {
	Status  string
	RuleKey string
	Limit   int
	Cursor  *DeliveryCursor
}

// DeliveryCursor is the keyset position (created_at, id) of the last row served.
type DeliveryCursor struct {
	CreatedAt time.Time
	ID        string
}

// automationDeliveryStore is the persistence the delivery log needs (consumer-side).
type automationDeliveryStore interface {
	List(ctx context.Context, f DeliveryFilter) ([]AutomationDelivery, string, error)
}

// pgAutomationDeliveryStore implements automationDeliveryStore on a tenant pool.
type pgAutomationDeliveryStore struct{ pool *pgxpool.Pool }

const automationDeliverySelect = `
SELECT d.id::text, r.rule_key, d.resource, d.record_id, d.stage,
       to_char(d.anchor_date, 'YYYY-MM-DD'), d.recipient_email, d.status, d.reason,
       d.attempts, d.run_at,
       d.sent_at,
       d.last_error, d.created_at
FROM automation_deliveries d
JOIN automation_rules r ON r.id = d.rule_id`

// List returns up to f.Limit rows (newest first) plus the next cursor, empty on the last page.
func (s pgAutomationDeliveryStore) List(ctx context.Context, f DeliveryFilter) ([]AutomationDelivery, string, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Status != "" {
		add("d.status = ?", f.Status)
	}
	if f.RuleKey != "" {
		add("r.rule_key = ?", f.RuleKey)
	}
	if f.Cursor != nil {
		args = append(args, f.Cursor.CreatedAt, f.Cursor.ID)
		n := len(args)
		where = append(where, fmt.Sprintf("(d.created_at, d.id) < ($%d, $%d::uuid)", n-1, n))
	}
	q := automationDeliverySelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	q += fmt.Sprintf(" ORDER BY d.created_at DESC, d.id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list automation deliveries: %w", err)
	}
	defer rows.Close()
	out := []AutomationDelivery{}
	for rows.Next() {
		var d AutomationDelivery
		if err := rows.Scan(&d.ID, &d.RuleKey, &d.Resource, &d.RecordID, &d.Stage, &d.AnchorDate,
			&d.RecipientEmail, &d.Status, &d.Reason, &d.Attempts, &d.RunAt, &d.SentAt,
			&d.LastError, &d.CreatedAt); err != nil {
			return nil, "", fmt.Errorf("scan automation delivery: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate automation deliveries: %w", err)
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeDeliveryCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}
