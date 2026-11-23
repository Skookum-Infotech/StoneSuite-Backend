package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/glpost"
)

// Statuses that matter to the ledger. A payment is received once approved and
// banked once deposited; DEPO is terminal and cannot be voided.
const (
	ledgerApproved  = "APPV"
	ledgerDeposited = "DEPO"
	ledgerVoid      = "VOID"
)

func isReceived(code string) bool { return code == ledgerApproved || code == ledgerDeposited }

// syncLedger brings the general ledger in line with the payment's status after
// a change, inside the change's own transaction:
//   - becoming Approved posts the receipt (Dr Undeposited Funds, Cr Accounts
//     Receivable);
//   - becoming Deposited posts the deposit (Dr Bank, Cr Undeposited Funds) --
//     and, when the payment skipped Approved (no approver was configured),
//     the receipt first;
//   - a void reverses whatever was posted.
//
// prevCode is the status before the change ("" for a payment created already
// approved). A payment that was approved before posting existed has no receipt
// entry; deposit it later and it still posts nothing, because a deposit with no
// receipt behind it would drive Undeposited Funds negative. That keeps the
// "no backfill" rule whole.
func syncLedger(ctx context.Context, tx pgx.Tx, internalID, actorEmployeeID int, prevCode string) error {
	var uuid, number, code string
	var date time.Time
	var amount float64
	if err := tx.QueryRow(ctx, `
		SELECT p.payment_uuid::text, COALESCE(p.payment_number, ''), rs.record_status_code, p.payment_date, p.payment_amount
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_id = $1`, internalID,
	).Scan(&uuid, &number, &code, &date, &amount); err != nil {
		return fmt.Errorf("load payment for ledger: %w", err)
	}
	cents := glpost.Cents(amount)
	post := func(source string, legs []glpost.Leg, memo string) error {
		_, err := glpost.PostOnce(ctx, tx, glpost.Posting{
			SourceType: source, SourceID: uuid, Date: date, Memo: memo + " " + number, Actor: actorEmployeeID, Legs: legs,
		})
		return err
	}

	var err error
	switch {
	case code == ledgerVoid:
		for _, source := range []string{glpost.SourcePaymentDeposit, glpost.SourcePayment} {
			if _, err = glpost.ReverseIfPosted(ctx, tx, source, uuid, glpost.Today(), "Void of payment "+number, actorEmployeeID); err != nil {
				break
			}
		}
	case isReceived(code) && !isReceived(prevCode):
		if err = post(glpost.SourcePayment, glpost.PaymentReceivedLegs(cents), "Payment"); err == nil && code == ledgerDeposited {
			err = post(glpost.SourcePaymentDeposit, glpost.PaymentDepositLegs(cents), "Deposit of payment")
		}
	case code == ledgerDeposited && prevCode == ledgerApproved:
		var received bool
		if received, err = glpost.HasEntry(ctx, tx, glpost.SourcePayment, uuid); err == nil && received {
			err = post(glpost.SourcePaymentDeposit, glpost.PaymentDepositLegs(cents), "Deposit of payment")
		}
	}
	if ue, ok := glpost.AsUserError(err); ok {
		return ClientError{Msg: ue.Msg}
	}
	return err
}
