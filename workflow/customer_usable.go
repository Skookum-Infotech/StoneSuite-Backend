// workflow/customer_usable.go
//
// The rule behind "a customer can be used on other records": only an Active
// customer can be attached to a NEW Estimate, Quote, Sales Order, Invoice,
// Payment, Refund or Credit Memo. Draft (not yet approved or activated),
// Inactive and Credit Hold customers cannot. The customer picker on the
// frontend already lists only Active customers; this is the server-side half,
// so an API call, a Convert or a stale form can't get around it.
//
// It lives here, not in crmstore, because every document module already imports
// workflow and crmstore imports several of them. Each module keeps its own
// ClientError type, so these helpers return a message and let the caller wrap it
// in its own error rather than returning an error type of their own.
//
// Deliberately narrow:
//   - it is checked when a document is CREATED (including a Convert, which creates
//     one), never when an existing document is edited, moved or paid, so putting a
//     customer on hold does not freeze the documents already open for them;
//   - a customer with no status at all is treated as usable. Every customer made
//     through the CRM has one, so that only ever means a hand-inserted row, and
//     refusing it would say nothing useful to the user.
package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CustomerStatusActive is the code (lkp_crm_status.crm_status_code) of the one
// customer status that can be used on other records. crmstore's customer status
// flow is built on the same constant.
const CustomerStatusActive = "CACT"

// customerUsableSQL is shared by the two lookups below; only the WHERE differs.
const customerUsableSQL = `
	SELECT COALESCE(cs.crm_status_code, ''), COALESCE(cs.crm_status_name, '')
	FROM customer c
	LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status
	WHERE c.customer_deleted_at IS NULL AND `

// CustomerNotUsableByUUID returns the reason the customer with this uuid can't
// be used on a new record, or "" when it can. An unknown or deleted customer
// also returns "": the caller already reports that with its own message.
func CustomerNotUsableByUUID(ctx context.Context, q Querier, customerUUID string) (string, error) {
	return customerNotUsable(ctx, q, customerUsableSQL+`c.customer_uuid = $1`, customerUUID)
}

// CustomerNotUsableByID is CustomerNotUsableByUUID for a caller that holds the
// customer's internal id -- a Convert, which copies it off the source document.
func CustomerNotUsableByID(ctx context.Context, q Querier, customerID int) (string, error) {
	return customerNotUsable(ctx, q, customerUsableSQL+`c.customer_id = $1`, customerID)
}

func customerNotUsable(ctx context.Context, q Querier, sql string, arg any) (string, error) {
	var code, name string
	err := q.QueryRow(ctx, sql, arg).Scan(&code, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check customer status: %w", err)
	}
	return customerNotUsableMessage(code, name), nil
}

// customerNotUsableMessage is the pure rule: "" when the status is usable, else
// what to tell the user.
func customerNotUsableMessage(statusCode, statusName string) string {
	if statusCode == "" || statusCode == CustomerStatusActive {
		return ""
	}
	return fmt.Sprintf("This customer's status is %s. Only Active customers can be used on new records.", statusName)
}
