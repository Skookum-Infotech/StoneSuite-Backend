// Package prospect provides the data model and database layer for the
// first-class CRM Prospects entity. Records are stored in the per-tenant
// `prospects` table (migration 000004) with typed columns for every
// NetSuite-style form field, rather than in the generic workflow_records JSONB.
package prospect

import "time"

// Prospect is the full CRM prospect record. JSON keys use snake_case to match
// the form field keys in prospectForm.ts so the frontend can spread the API
// response directly into the read-only details view.
type Prospect struct {
	ID string `json:"id"`

	// Primary Information
	CustomForm         string `json:"custom_form"`
	Status             string `json:"status"`
	Comments           string `json:"comments"`
	CustomerID         string `json:"customer_id"`
	CustomerIDAuto     bool   `json:"customer_id_auto"`
	ParentCompany      string `json:"parent_company"`
	SFDCCustomerStatus string `json:"sfdc_customer_status"`
	CompanyName        string `json:"company_name"`
	ZuoraInvoiceName   string `json:"zuora_invoice_name"`
	AccountStatus      string `json:"account_status"`
	CustomerType       string `json:"customer_type"`
	ARStatus           string `json:"ar_status"`
	BillingAccountName string `json:"billing_account_name"`

	// Email | Phone | Address
	Email                 string `json:"email"`
	Phone                 string `json:"phone"`
	Address               string `json:"address"`
	MultipleEmailInvoices string `json:"multiple_email_invoices"`
	AltPhone              string `json:"alt_phone"`

	// Classification
	Subsidiary         string `json:"subsidiary"`
	TalkdeskRegion     string `json:"talkdesk_region"`
	TalkdeskIDPlatform string `json:"talkdesk_id_platform"`
	WebAddress         string `json:"web_address"`
	CRMAccountOwner    string `json:"crm_account_owner"`
	ARAnalyst          string `json:"ar_analyst"`
	CRMCSM             string `json:"crm_csm"`
	CRMCSMTeam         string `json:"crm_csm_team"`
	CRMGrowthManager   string `json:"crm_growth_manager"`
	WhiteGlove         bool   `json:"white_glove"`
	DisplayProductCode bool   `json:"display_product_code"`

	// Sales
	Territory       string   `json:"territory"`
	EstimatedBudget *float64 `json:"estimated_budget"`
	BudgetApproved  bool     `json:"budget_approved"`
	SalesReadiness  string   `json:"sales_readiness"`
	BuyingReason    string   `json:"buying_reason"`
	BuyingTimeFrame string   `json:"buying_time_frame"`

	// Financial
	CreditLimit  *float64 `json:"credit_limit"`
	PaymentTerms string   `json:"payment_terms"`
	Currency     string   `json:"currency"`
	TaxID        string   `json:"tax_id"`

	// Subsidiaries
	PrimarySubsidiary   string   `json:"primary_subsidiary"`
	ConsolidatedBalance *float64 `json:"consolidated_balance"`

	// Address tab
	DefaultBillingAddress  string `json:"default_billing_address"`
	DefaultShippingAddress string `json:"default_shipping_address"`

	// Relationships
	SalesRep       string `json:"sales_rep"`
	Partner        string `json:"partner"`
	PrimaryContact string `json:"primary_contact"`
	ContactRole    string `json:"contact_role"`

	// Communication
	PreferredChannel string `json:"preferred_channel"`
	EmailPreference  string `json:"email_preference"`
	UnsubscribeAll   bool   `json:"unsubscribe_all"`

	// ZAB Subscriptions
	ZABAccountID     string `json:"zab_account_id"`
	SubscriptionPlan string `json:"subscription_plan"`
	BillingCycle     string `json:"billing_cycle"`

	// Zuora Sync Details
	ZuoraAccountID string `json:"zuora_account_id"`
	SyncStatus     string `json:"sync_status"`
	LastSynced     string `json:"last_synced"`

	// Zuora Account
	ZuoraAccountNumber string   `json:"zuora_account_number"`
	ZuoraBalance       *float64 `json:"zuora_balance"`
	ZuoraAutoPay       bool     `json:"zuora_auto_pay"`

	// Stripe
	StripeCustomerID    string `json:"stripe_customer_id"`
	StripePaymentMethod string `json:"stripe_payment_method"`
	StripeCurrency      string `json:"stripe_currency"`

	// CCH SureTax
	SureTaxCustomerNumber string `json:"suretax_customer_number"`
	TaxExempt             bool   `json:"tax_exempt"`
	ExemptionCertificate  string `json:"exemption_certificate"`

	// E-Document
	EdocEnabled bool   `json:"edoc_enabled"`
	EdocFormat  string `json:"edoc_format"`
	EdocEmail   string `json:"edoc_email"`

	// Custom
	CustomField1 string `json:"custom_field_1"`
	CustomField2 string `json:"custom_field_2"`
	CustomNotes  string `json:"custom_notes"`

	// Preferences
	Language          string `json:"language"`
	Timezone          string `json:"timezone"`
	DateFormat        string `json:"date_format"`
	ReceiveNewsletter bool   `json:"receive_newsletter"`

	// Dynamic custom fields (from workflow_field_definitions for the prospect workflow).
	CustomFields map[string]any `json:"customFields,omitempty"`

	// Meta
	OwnerUserID string    `json:"owner_user_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
