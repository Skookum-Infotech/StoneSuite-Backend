// Package glpost posts sales and purchasing documents to the general ledger.
//
// Until this package existed only manual journal entries and cash transfers
// reached the ledger, so every ledger-backed report (Profit & Loss above all)
// saw none of the business's invoices, payments or bills. A document module
// calls Sync-style helpers built on this package inside the same transaction
// that changes the document's status, so the ledger entry commits or rolls
// back with it (journal's own atomicity rule).
//
// Design rules this package holds to:
//   - Idempotent. An entry is keyed by (source_type, source_id); posting a
//     document that already has a live entry does nothing, and reversing one
//     that is not posted (or already reversed) does nothing. That is what lets
//     every status-changing path call it without coordinating.
//   - No backfill. A document approved before this existed has no entry;
//     voiding it reverses nothing.
//   - Accounts come only from the tenant's default-account slots
//     (coa_default_mapping), never hard-coded, so a tenant's own chart works.
//   - Amounts are integer cents end to end; an entry is balanced by
//     construction (one leg is the plug), never by floating-point luck.
package glpost

// ApprovalAction is the action string approvalchain passes a module's AuditTx
// hook when an approval commits; a module's hook syncs the ledger on exactly it.
const ApprovalAction = "approve"

// Source types stamped on journal_entry.source_type (VARCHAR(30), no FK).
const (
	SourceInvoice        = "invoice"
	SourcePayment        = "payment"
	SourcePaymentDeposit = "payment_deposit"
	SourceVendorBill     = "vendor_bill"
	SourceVendorPayment  = "vendor_payment"
)

// Default-account slot keys (coa_default_mapping.slot_key).
const (
	SlotAR            = "default_ar"
	SlotAP            = "default_ap"
	SlotSalesRevenue  = "default_sales_revenue"
	SlotSalesDiscount = "default_sales_discount"
	SlotTaxPayable    = "default_sales_tax_payable"
	SlotTaxReceivable = "default_sales_tax_receivable"
	SlotCOGS          = "default_cogs"
	SlotBank          = "default_bank"
	SlotUndeposited   = "default_undeposited_funds"
)

// slotLabels name a slot for the person who has to map it.
var slotLabels = map[string]string{
	SlotAR:            "Accounts Receivable",
	SlotAP:            "Accounts Payable",
	SlotSalesRevenue:  "Sales Revenue",
	SlotSalesDiscount: "Sales Discount",
	SlotTaxPayable:    "Sales Tax Payable",
	SlotTaxReceivable: "Sales Tax Receivable",
	SlotCOGS:          "Cost of Goods Sold",
	SlotBank:          "Bank",
	SlotUndeposited:   "Undeposited Funds",
}

// Leg is one side of a posting against a default-account slot, in cents. Exactly
// one of DebitCents/CreditCents is non-zero.
type Leg struct {
	Slot        string
	DebitCents  int64
	CreditCents int64
}

func debit(slot string, cents int64) Leg  { return Leg{Slot: slot, DebitCents: cents} }
func credit(slot string, cents int64) Leg { return Leg{Slot: slot, CreditCents: cents} }

// InvoiceAmounts are an invoice's stored money columns, in cents.
type InvoiceAmounts struct {
	Discount, Tax, Grand int64
}

// InvoiceLegs: Dr Accounts Receivable for the whole invoice (and Dr Sales
// Discount for any discount given); Cr Sales Tax Payable for the tax; Cr Sales
// Revenue for the rest. Revenue is the plug -- grand + discount - tax -- so
// shipping, an adjustment or a rounding difference lands in revenue and the
// entry always balances.
func InvoiceLegs(a InvoiceAmounts) []Leg {
	return []Leg{
		debit(SlotAR, a.Grand),
		debit(SlotSalesDiscount, a.Discount),
		credit(SlotTaxPayable, a.Tax),
		credit(SlotSalesRevenue, a.Grand+a.Discount-a.Tax),
	}
}

// BillAmounts are a vendor bill's stored money columns, in cents.
type BillAmounts struct {
	Tax, Grand int64
}

// BillLegs: Cr Accounts Payable for the whole bill; Dr Sales Tax Receivable for
// the recoverable tax; Dr Cost of Goods Sold for the rest (the plug, so a
// discount or adjustment is already netted out).
func BillLegs(a BillAmounts) []Leg {
	return []Leg{
		credit(SlotAP, a.Grand),
		debit(SlotTaxReceivable, a.Tax),
		debit(SlotCOGS, a.Grand-a.Tax),
	}
}

// PaymentReceivedLegs: a customer payment is approved -- Dr Undeposited Funds,
// Cr Accounts Receivable.
func PaymentReceivedLegs(amount int64) []Leg {
	return []Leg{debit(SlotUndeposited, amount), credit(SlotAR, amount)}
}

// PaymentDepositLegs: the payment is deposited -- Dr Bank, Cr Undeposited Funds.
func PaymentDepositLegs(amount int64) []Leg {
	return []Leg{debit(SlotBank, amount), credit(SlotUndeposited, amount)}
}

// VendorPaymentLegs: a vendor payment is paid -- Dr Accounts Payable, Cr Bank.
func VendorPaymentLegs(amount int64) []Leg {
	return []Leg{debit(SlotAP, amount), credit(SlotBank, amount)}
}
