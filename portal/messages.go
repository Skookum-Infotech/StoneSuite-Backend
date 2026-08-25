package portal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrMessageDocumentNotVisible is returned when a message is addressed to a
// document the caller may not see. Handlers map it to 404, not 403, so a
// document id cannot be probed for existence through the message endpoint.
var ErrMessageDocumentNotVisible = errors.New("document not visible to this customer")

// maxMessageBody caps a single message. Generous for a question about an
// invoice, small enough that the endpoint is not a storage vector.
const maxMessageBody = 4000

// Message is one entry in a document's thread.
//
// AuthorName is resolved for display: the portal user's name for a customer
// message, the employee's name for a staff reply. AuthorKind tells the client
// which side of the conversation it is, so it never has to infer that from a
// name.
type Message struct {
	UUID         string    `json:"id"`
	Module       string    `json:"module"`
	DocumentUUID string    `json:"documentId"`
	AuthorKind   string    `json:"authorKind"`
	AuthorName   string    `json:"authorName"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ListMessages returns a document's thread, oldest first.
//
// customerID scopes the read: a portal caller sees only threads on its own
// documents. Staff callers pass the document's customer id, which they have
// already established through their own permission and scope checks.
func ListMessages(ctx context.Context, q Querier, customerID int, module Module, documentUUID string) ([]Message, error) {
	rows, err := q.Query(ctx, `
		SELECT pm.portal_message_uuid, pm.portal_message_module, pm.portal_message_document_uuid,
		       pm.portal_message_author_kind,
		       COALESCE(cpu.full_name, NULLIF(TRIM(e.employee_first_name || ' ' || e.employee_last_name), ''), ''),
		       pm.portal_message_body, pm.portal_message_created_at
		FROM portal_message pm
		LEFT JOIN customer_portal_user cpu ON cpu.id = pm.portal_message_author_portal_user_id
		LEFT JOIN employee e               ON e.employee_id = pm.portal_message_author_employee_id
		WHERE pm.portal_message_customer_id   = $1
		  AND pm.portal_message_module        = $2
		  AND pm.portal_message_document_uuid = $3
		  AND pm.portal_message_deleted_at IS NULL
		ORDER BY pm.portal_message_created_at`,
		customerID, string(module), documentUUID)
	if err != nil {
		return nil, fmt.Errorf("list portal messages: %w", err)
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.UUID, &m.Module, &m.DocumentUUID, &m.AuthorKind,
			&m.AuthorName, &m.Body, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan portal message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate portal messages: %w", err)
	}
	return out, nil
}

// CreatePortalMessage records a customer's message on one of their documents.
//
// The caller must have already confirmed the document is visible to this
// customer (by loading it through the module's PortalGet). customerID is taken
// from the session, never from the request, so a message cannot be attached to
// another customer's document even if the document id is guessed.
func CreatePortalMessage(ctx context.Context, q Querier, customerID int, portalUserID string,
	module Module, documentUUID, body string) (*Message, error) {
	if err := validateBody(body); err != nil {
		return nil, err
	}
	var m Message
	err := q.QueryRow(ctx, `
		INSERT INTO portal_message (portal_message_module, portal_message_document_uuid,
			portal_message_customer_id, portal_message_author_kind,
			portal_message_author_portal_user_id, portal_message_body)
		VALUES ($1, $2, $3, 'portal', $4, $5)
		RETURNING portal_message_uuid, portal_message_module, portal_message_document_uuid,
		          portal_message_author_kind, portal_message_body, portal_message_created_at`,
		string(module), documentUUID, customerID, portalUserID, body,
	).Scan(&m.UUID, &m.Module, &m.DocumentUUID, &m.AuthorKind, &m.Body, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create portal message: %w", err)
	}
	return &m, nil
}

// CreateStaffMessage records an internal reply on a customer's document thread.
func CreateStaffMessage(ctx context.Context, q Querier, customerID, employeeID int,
	module Module, documentUUID, body string) (*Message, error) {
	if err := validateBody(body); err != nil {
		return nil, err
	}
	var m Message
	err := q.QueryRow(ctx, `
		INSERT INTO portal_message (portal_message_module, portal_message_document_uuid,
			portal_message_customer_id, portal_message_author_kind,
			portal_message_author_employee_id, portal_message_body)
		VALUES ($1, $2, $3, 'staff', $4, $5)
		RETURNING portal_message_uuid, portal_message_module, portal_message_document_uuid,
		          portal_message_author_kind, portal_message_body, portal_message_created_at`,
		string(module), documentUUID, customerID, employeeID, body,
	).Scan(&m.UUID, &m.Module, &m.DocumentUUID, &m.AuthorKind, &m.Body, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create staff message: %w", err)
	}
	return &m, nil
}

// DocumentCustomerID resolves which customer a document belongs to.
//
// Used by the staff reply endpoint, which knows the document but not the
// customer, and to verify a portal message targets a document that exists.
// An unknown module yields ErrMessageDocumentNotVisible rather than a query,
// so the table name can only ever come from the switch below.
func DocumentCustomerID(ctx context.Context, q Querier, module Module, documentUUID string) (int, error) {
	var col, table string
	switch module {
	case ModuleSalesOrder:
		table, col = "sales_order", "sales_order_customer_id"
	case ModuleInvoice:
		table, col = "invoice", "invoice_customer_id"
	case ModulePayment:
		table, col = "payment", "payment_customer_id"
	case ModuleRefund:
		table, col = "refund", "refund_customer_id"
	default:
		return 0, ErrMessageDocumentNotVisible
	}
	// table/col come from the switch above, never from caller input, so this
	// interpolation cannot carry untrusted text. The uuid is parameterized.
	var id int
	err := q.QueryRow(ctx,
		fmt.Sprintf(`SELECT %s FROM %s WHERE %s_uuid = $1`, col, table, table), documentUUID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrMessageDocumentNotVisible
	}
	if err != nil {
		return 0, fmt.Errorf("resolve document customer: %w", err)
	}
	return id, nil
}

// BodyError marks a caller-fault message body (maps to HTTP 400).
type BodyError struct{ Msg string }

func (e BodyError) Error() string { return e.Msg }

func validateBody(body string) error {
	if len(body) == 0 {
		return BodyError{Msg: "Message body is required."}
	}
	if len(body) > maxMessageBody {
		return BodyError{Msg: fmt.Sprintf("Message must be %d characters or fewer.", maxMessageBody)}
	}
	return nil
}
