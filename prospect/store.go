package prospect

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a prospect row does not exist.
var ErrNotFound = errors.New("prospect not found")

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// UserIDByIdentity resolves the tenant-local user id from a control-plane
// identity id. Mirrors the same query in the workflow package so the prospect
// controller can set owner_user_id without importing the workflow package.
func UserIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM users WHERE identity_id = $1`, identityID).Scan(&id)
	return id, err
}

// ----- input helpers ---------------------------------------------------------

// strVal reads a string value from a form-data map; returns "" when absent.
func strVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// boolVal reads a boolean from a form-data map; returns false when absent.
func boolVal(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// floatPtrVal reads an optional numeric value. The form sends numbers as
// strings (HTML input type="number" onChange stores e.target.value as string);
// Go's json decoder may also produce float64 for JSON numbers.
func floatPtrVal(m map[string]any, key string) *float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		return &t
	case string:
		if t == "" {
			return nil
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return nil
		}
		return &f
	}
	return nil
}

// FromMap converts a raw form-data map (as received from the frontend) into a
// Prospect ready for insertion. ownerUserID may be "" if lookup failed.
func FromMap(ownerUserID string, m map[string]any) Prospect {
	return Prospect{
		OwnerUserID: ownerUserID,

		CustomForm:         strVal(m, "custom_form"),
		Status:             strVal(m, "status"),
		Comments:           strVal(m, "comments"),
		CustomerID:         strVal(m, "customer_id"),
		CustomerIDAuto:     boolVal(m, "customer_id_auto"),
		ParentCompany:      strVal(m, "parent_company"),
		SFDCCustomerStatus: strVal(m, "sfdc_customer_status"),
		CompanyName:        strVal(m, "company_name"),
		ZuoraInvoiceName:   strVal(m, "zuora_invoice_name"),
		AccountStatus:      strVal(m, "account_status"),
		CustomerType:       strVal(m, "customer_type"),
		ARStatus:           strVal(m, "ar_status"),
		BillingAccountName: strVal(m, "billing_account_name"),

		Email:                 strVal(m, "email"),
		Phone:                 strVal(m, "phone"),
		Address:               strVal(m, "address"),
		MultipleEmailInvoices: strVal(m, "multiple_email_invoices"),
		AltPhone:              strVal(m, "alt_phone"),

		Subsidiary:         strVal(m, "subsidiary"),
		TalkdeskRegion:     strVal(m, "talkdesk_region"),
		TalkdeskIDPlatform: strVal(m, "talkdesk_id_platform"),
		WebAddress:         strVal(m, "web_address"),
		CRMAccountOwner:    strVal(m, "crm_account_owner"),
		ARAnalyst:          strVal(m, "ar_analyst"),
		CRMCSM:             strVal(m, "crm_csm"),
		CRMCSMTeam:         strVal(m, "crm_csm_team"),
		CRMGrowthManager:   strVal(m, "crm_growth_manager"),
		WhiteGlove:         boolVal(m, "white_glove"),
		DisplayProductCode: boolVal(m, "display_product_code"),

		Territory:       strVal(m, "territory"),
		EstimatedBudget: floatPtrVal(m, "estimated_budget"),
		BudgetApproved:  boolVal(m, "budget_approved"),
		SalesReadiness:  strVal(m, "sales_readiness"),
		BuyingReason:    strVal(m, "buying_reason"),
		BuyingTimeFrame: strVal(m, "buying_time_frame"),

		CreditLimit:  floatPtrVal(m, "credit_limit"),
		PaymentTerms: strVal(m, "payment_terms"),
		Currency:     strVal(m, "currency"),
		TaxID:        strVal(m, "tax_id"),

		PrimarySubsidiary:   strVal(m, "primary_subsidiary"),
		ConsolidatedBalance: floatPtrVal(m, "consolidated_balance"),

		DefaultBillingAddress:  strVal(m, "default_billing_address"),
		DefaultShippingAddress: strVal(m, "default_shipping_address"),

		SalesRep:       strVal(m, "sales_rep"),
		Partner:        strVal(m, "partner"),
		PrimaryContact: strVal(m, "primary_contact"),
		ContactRole:    strVal(m, "contact_role"),

		PreferredChannel: strVal(m, "preferred_channel"),
		EmailPreference:  strVal(m, "email_preference"),
		UnsubscribeAll:   boolVal(m, "unsubscribe_all"),

		ZABAccountID:     strVal(m, "zab_account_id"),
		SubscriptionPlan: strVal(m, "subscription_plan"),
		BillingCycle:     strVal(m, "billing_cycle"),

		ZuoraAccountID: strVal(m, "zuora_account_id"),
		SyncStatus:     strVal(m, "sync_status"),
		LastSynced:     strVal(m, "last_synced"),

		ZuoraAccountNumber: strVal(m, "zuora_account_number"),
		ZuoraBalance:       floatPtrVal(m, "zuora_balance"),
		ZuoraAutoPay:       boolVal(m, "zuora_auto_pay"),

		StripeCustomerID:    strVal(m, "stripe_customer_id"),
		StripePaymentMethod: strVal(m, "stripe_payment_method"),
		StripeCurrency:      strVal(m, "stripe_currency"),

		SureTaxCustomerNumber: strVal(m, "suretax_customer_number"),
		TaxExempt:             boolVal(m, "tax_exempt"),
		ExemptionCertificate:  strVal(m, "exemption_certificate"),

		EdocEnabled: boolVal(m, "edoc_enabled"),
		EdocFormat:  strVal(m, "edoc_format"),
		EdocEmail:   strVal(m, "edoc_email"),

		CustomField1: strVal(m, "custom_field_1"),
		CustomField2: strVal(m, "custom_field_2"),
		CustomNotes:  strVal(m, "custom_notes"),

		Language:          strVal(m, "language"),
		Timezone:          strVal(m, "timezone"),
		DateFormat:        strVal(m, "date_format"),
		ReceiveNewsletter: boolVal(m, "receive_newsletter"),
	}
}

// ----- DB column list --------------------------------------------------------

// selectCols is the ordered column list used in every SELECT. The order must
// match the scan destinations in scanRow exactly.
const selectCols = `
	id, owner_user_id,
	custom_form, status, comments, customer_id, customer_id_auto, parent_company,
	sfdc_customer_status, company_name, zuora_invoice_name, account_status,
	customer_type, ar_status, billing_account_name,
	email, phone, address, multiple_email_invoices, alt_phone,
	subsidiary, talkdesk_region, talkdesk_id_platform, web_address,
	crm_account_owner, ar_analyst, crm_csm, crm_csm_team, crm_growth_manager,
	white_glove, display_product_code,
	territory, estimated_budget, budget_approved, sales_readiness,
	buying_reason, buying_time_frame,
	credit_limit, payment_terms, currency, tax_id,
	primary_subsidiary, consolidated_balance,
	default_billing_address, default_shipping_address,
	sales_rep, partner, primary_contact, contact_role,
	preferred_channel, email_preference, unsubscribe_all,
	zab_account_id, subscription_plan, billing_cycle,
	zuora_account_id, sync_status, last_synced,
	zuora_account_number, zuora_balance, zuora_auto_pay,
	stripe_customer_id, stripe_payment_method, stripe_currency,
	suretax_customer_number, tax_exempt, exemption_certificate,
	edoc_enabled, edoc_format, edoc_email,
	custom_field_1, custom_field_2, custom_notes,
	language, timezone, date_format, receive_newsletter,
	created_at, updated_at`

// scanner is satisfied by both pgx.Row and pgx.Rows, letting scanRow work
// for QueryRow (single result) and the inner loop of Query (multi-row).
type scanner interface {
	Scan(dest ...any) error
}

// scanRow populates a Prospect from any scanner (pgx.Row or pgx.Rows).
// The column order must match selectCols exactly.
func scanRow(row scanner) (*Prospect, error) {
	var p Prospect
	var ownerID *string
	err := row.Scan(
		&p.ID, &ownerID,
		&p.CustomForm, &p.Status, &p.Comments, &p.CustomerID, &p.CustomerIDAuto, &p.ParentCompany,
		&p.SFDCCustomerStatus, &p.CompanyName, &p.ZuoraInvoiceName, &p.AccountStatus,
		&p.CustomerType, &p.ARStatus, &p.BillingAccountName,
		&p.Email, &p.Phone, &p.Address, &p.MultipleEmailInvoices, &p.AltPhone,
		&p.Subsidiary, &p.TalkdeskRegion, &p.TalkdeskIDPlatform, &p.WebAddress,
		&p.CRMAccountOwner, &p.ARAnalyst, &p.CRMCSM, &p.CRMCSMTeam, &p.CRMGrowthManager,
		&p.WhiteGlove, &p.DisplayProductCode,
		&p.Territory, &p.EstimatedBudget, &p.BudgetApproved, &p.SalesReadiness,
		&p.BuyingReason, &p.BuyingTimeFrame,
		&p.CreditLimit, &p.PaymentTerms, &p.Currency, &p.TaxID,
		&p.PrimarySubsidiary, &p.ConsolidatedBalance,
		&p.DefaultBillingAddress, &p.DefaultShippingAddress,
		&p.SalesRep, &p.Partner, &p.PrimaryContact, &p.ContactRole,
		&p.PreferredChannel, &p.EmailPreference, &p.UnsubscribeAll,
		&p.ZABAccountID, &p.SubscriptionPlan, &p.BillingCycle,
		&p.ZuoraAccountID, &p.SyncStatus, &p.LastSynced,
		&p.ZuoraAccountNumber, &p.ZuoraBalance, &p.ZuoraAutoPay,
		&p.StripeCustomerID, &p.StripePaymentMethod, &p.StripeCurrency,
		&p.SureTaxCustomerNumber, &p.TaxExempt, &p.ExemptionCertificate,
		&p.EdocEnabled, &p.EdocFormat, &p.EdocEmail,
		&p.CustomField1, &p.CustomField2, &p.CustomNotes,
		&p.Language, &p.Timezone, &p.DateFormat, &p.ReceiveNewsletter,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if ownerID != nil {
		p.OwnerUserID = *ownerID
	}
	return &p, nil
}

// ownerArg converts an empty-string owner to nil so Postgres stores NULL.
func ownerArg(ownerUserID string) any {
	if ownerUserID == "" {
		return nil
	}
	return ownerUserID
}

// ----- CRUD ------------------------------------------------------------------

// List returns all prospects for the tenant, newest first.
func List(ctx context.Context, q Querier) ([]Prospect, error) {
	rows, err := q.Query(ctx,
		`SELECT `+selectCols+` FROM prospects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list prospects: %w", err)
	}
	defer rows.Close()

	var out []Prospect
	for rows.Next() {
		p, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan prospect: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Get fetches one prospect by id.
func Get(ctx context.Context, q Querier, id string) (*Prospect, error) {
	row := q.QueryRow(ctx,
		`SELECT `+selectCols+` FROM prospects WHERE id = $1`, id)
	p, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get prospect: %w", err)
	}
	return p, nil
}

// Create inserts a new prospect and returns the full row.
func Create(ctx context.Context, q Querier, p Prospect) (*Prospect, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO prospects (
			owner_user_id,
			custom_form, status, comments, customer_id, customer_id_auto, parent_company,
			sfdc_customer_status, company_name, zuora_invoice_name, account_status,
			customer_type, ar_status, billing_account_name,
			email, phone, address, multiple_email_invoices, alt_phone,
			subsidiary, talkdesk_region, talkdesk_id_platform, web_address,
			crm_account_owner, ar_analyst, crm_csm, crm_csm_team, crm_growth_manager,
			white_glove, display_product_code,
			territory, estimated_budget, budget_approved, sales_readiness,
			buying_reason, buying_time_frame,
			credit_limit, payment_terms, currency, tax_id,
			primary_subsidiary, consolidated_balance,
			default_billing_address, default_shipping_address,
			sales_rep, partner, primary_contact, contact_role,
			preferred_channel, email_preference, unsubscribe_all,
			zab_account_id, subscription_plan, billing_cycle,
			zuora_account_id, sync_status, last_synced,
			zuora_account_number, zuora_balance, zuora_auto_pay,
			stripe_customer_id, stripe_payment_method, stripe_currency,
			suretax_customer_number, tax_exempt, exemption_certificate,
			edoc_enabled, edoc_format, edoc_email,
			custom_field_1, custom_field_2, custom_notes,
			language, timezone, date_format, receive_newsletter
		) VALUES (
			$1,
			$2,$3,$4,$5,$6,$7,
			$8,$9,$10,$11,
			$12,$13,$14,
			$15,$16,$17,$18,$19,
			$20,$21,$22,$23,
			$24,$25,$26,$27,$28,
			$29,$30,
			$31,$32,$33,$34,
			$35,$36,
			$37,$38,$39,$40,
			$41,$42,
			$43,$44,
			$45,$46,$47,$48,
			$49,$50,$51,
			$52,$53,$54,
			$55,$56,$57,
			$58,$59,$60,
			$61,$62,$63,
			$64,$65,$66,
			$67,$68,$69,
			$70,$71,$72,
			$73,$74,$75,$76
		) RETURNING `+selectCols,
		ownerArg(p.OwnerUserID),
		p.CustomForm, p.Status, p.Comments, p.CustomerID, p.CustomerIDAuto, p.ParentCompany,
		p.SFDCCustomerStatus, p.CompanyName, p.ZuoraInvoiceName, p.AccountStatus,
		p.CustomerType, p.ARStatus, p.BillingAccountName,
		p.Email, p.Phone, p.Address, p.MultipleEmailInvoices, p.AltPhone,
		p.Subsidiary, p.TalkdeskRegion, p.TalkdeskIDPlatform, p.WebAddress,
		p.CRMAccountOwner, p.ARAnalyst, p.CRMCSM, p.CRMCSMTeam, p.CRMGrowthManager,
		p.WhiteGlove, p.DisplayProductCode,
		p.Territory, p.EstimatedBudget, p.BudgetApproved, p.SalesReadiness,
		p.BuyingReason, p.BuyingTimeFrame,
		p.CreditLimit, p.PaymentTerms, p.Currency, p.TaxID,
		p.PrimarySubsidiary, p.ConsolidatedBalance,
		p.DefaultBillingAddress, p.DefaultShippingAddress,
		p.SalesRep, p.Partner, p.PrimaryContact, p.ContactRole,
		p.PreferredChannel, p.EmailPreference, p.UnsubscribeAll,
		p.ZABAccountID, p.SubscriptionPlan, p.BillingCycle,
		p.ZuoraAccountID, p.SyncStatus, p.LastSynced,
		p.ZuoraAccountNumber, p.ZuoraBalance, p.ZuoraAutoPay,
		p.StripeCustomerID, p.StripePaymentMethod, p.StripeCurrency,
		p.SureTaxCustomerNumber, p.TaxExempt, p.ExemptionCertificate,
		p.EdocEnabled, p.EdocFormat, p.EdocEmail,
		p.CustomField1, p.CustomField2, p.CustomNotes,
		p.Language, p.Timezone, p.DateFormat, p.ReceiveNewsletter,
	)
	created, err := scanRow(row)
	if err != nil {
		return nil, fmt.Errorf("create prospect: %w", err)
	}
	return created, nil
}

// Update replaces all mutable fields on an existing prospect.
func Update(ctx context.Context, q Querier, id string, p Prospect) (*Prospect, error) {
	row := q.QueryRow(ctx, `
		UPDATE prospects SET
			custom_form=$2, status=$3, comments=$4, customer_id=$5, customer_id_auto=$6,
			parent_company=$7, sfdc_customer_status=$8, company_name=$9,
			zuora_invoice_name=$10, account_status=$11, customer_type=$12,
			ar_status=$13, billing_account_name=$14,
			email=$15, phone=$16, address=$17, multiple_email_invoices=$18, alt_phone=$19,
			subsidiary=$20, talkdesk_region=$21, talkdesk_id_platform=$22, web_address=$23,
			crm_account_owner=$24, ar_analyst=$25, crm_csm=$26, crm_csm_team=$27,
			crm_growth_manager=$28, white_glove=$29, display_product_code=$30,
			territory=$31, estimated_budget=$32, budget_approved=$33, sales_readiness=$34,
			buying_reason=$35, buying_time_frame=$36,
			credit_limit=$37, payment_terms=$38, currency=$39, tax_id=$40,
			primary_subsidiary=$41, consolidated_balance=$42,
			default_billing_address=$43, default_shipping_address=$44,
			sales_rep=$45, partner=$46, primary_contact=$47, contact_role=$48,
			preferred_channel=$49, email_preference=$50, unsubscribe_all=$51,
			zab_account_id=$52, subscription_plan=$53, billing_cycle=$54,
			zuora_account_id=$55, sync_status=$56, last_synced=$57,
			zuora_account_number=$58, zuora_balance=$59, zuora_auto_pay=$60,
			stripe_customer_id=$61, stripe_payment_method=$62, stripe_currency=$63,
			suretax_customer_number=$64, tax_exempt=$65, exemption_certificate=$66,
			edoc_enabled=$67, edoc_format=$68, edoc_email=$69,
			custom_field_1=$70, custom_field_2=$71, custom_notes=$72,
			language=$73, timezone=$74, date_format=$75, receive_newsletter=$76,
			updated_at=NOW()
		WHERE id=$1
		RETURNING `+selectCols,
		id,
		p.CustomForm, p.Status, p.Comments, p.CustomerID, p.CustomerIDAuto,
		p.ParentCompany, p.SFDCCustomerStatus, p.CompanyName,
		p.ZuoraInvoiceName, p.AccountStatus, p.CustomerType,
		p.ARStatus, p.BillingAccountName,
		p.Email, p.Phone, p.Address, p.MultipleEmailInvoices, p.AltPhone,
		p.Subsidiary, p.TalkdeskRegion, p.TalkdeskIDPlatform, p.WebAddress,
		p.CRMAccountOwner, p.ARAnalyst, p.CRMCSM, p.CRMCSMTeam,
		p.CRMGrowthManager, p.WhiteGlove, p.DisplayProductCode,
		p.Territory, p.EstimatedBudget, p.BudgetApproved, p.SalesReadiness,
		p.BuyingReason, p.BuyingTimeFrame,
		p.CreditLimit, p.PaymentTerms, p.Currency, p.TaxID,
		p.PrimarySubsidiary, p.ConsolidatedBalance,
		p.DefaultBillingAddress, p.DefaultShippingAddress,
		p.SalesRep, p.Partner, p.PrimaryContact, p.ContactRole,
		p.PreferredChannel, p.EmailPreference, p.UnsubscribeAll,
		p.ZABAccountID, p.SubscriptionPlan, p.BillingCycle,
		p.ZuoraAccountID, p.SyncStatus, p.LastSynced,
		p.ZuoraAccountNumber, p.ZuoraBalance, p.ZuoraAutoPay,
		p.StripeCustomerID, p.StripePaymentMethod, p.StripeCurrency,
		p.SureTaxCustomerNumber, p.TaxExempt, p.ExemptionCertificate,
		p.EdocEnabled, p.EdocFormat, p.EdocEmail,
		p.CustomField1, p.CustomField2, p.CustomNotes,
		p.Language, p.Timezone, p.DateFormat, p.ReceiveNewsletter,
	)
	updated, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update prospect: %w", err)
	}
	return updated, nil
}

// Delete removes a prospect by id.
func Delete(ctx context.Context, q Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM prospects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete prospect: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
