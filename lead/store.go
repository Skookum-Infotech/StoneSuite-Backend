package lead

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a lead row does not exist.
var ErrNotFound = errors.New("lead not found")

// UserIDByIdentity resolves the tenant-local user id from a control-plane identity id.
func UserIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM users WHERE identity_id = $1`, identityID).Scan(&id)
	return id, err
}

// str reads a string value from a form-data map.
func str(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// mapVal reads a map[string]any from a form-data map (for customFields).
func mapVal(m map[string]any, key string) map[string]any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	mv, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return mv
}

// bval reads a boolean from a form-data map.
func bval(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// FromMap builds a Lead from the JSON body map sent by the frontend.
func FromMap(ownerUserID string, m map[string]any) Lead {
	l := Lead{
		OwnerUserID:                 ownerUserID,
		CustomForm:                  str(m, "customForm"),
		LeadStatus:                  str(m, "leadStatus"),
		DefaultOrderPriority:        str(m, "defaultOrderPriority"),
		Type:                        str(m, "type"),
		CompanyName:                 str(m, "companyName"),
		FirstName:                   str(m, "firstName"),
		LastName:                    str(m, "lastName"),
		Email:                       str(m, "email"),
		Phone:                       str(m, "phone"),
		Fax:                         str(m, "fax"),
		Address:                     str(m, "address"),
		SalesRep:                    str(m, "salesRep"),
		Territory:                   str(m, "territory"),
		Partner:                     str(m, "partner"),
		PrimarySubsidiary:           str(m, "primarySubsidiary"),
		EmailForPaymentNotification: str(m, "emailForPaymentNotification"),
		WhiteGlove:                  bval(m, "whiteGlove"),
		DisplayProductCode:          bval(m, "displayProductCode"),
		BlacklineArCashApp:          bval(m, "blacklineArCashApp"),
		SfdcAccountID:               str(m, "sfdcAccountId"),
		PrevExternalID:              str(m, "prevExternalId"),
		SfdcCustomerStatus:          str(m, "sfdcCustomerStatus"),
		CrmAccountOwner:             str(m, "crmAccountOwner"),
		CustomerLegalName:           str(m, "customerLegalName"),
		CustomerType:                str(m, "customerType"),
		CrmCsmTeam:                  str(m, "crmCsmTeam"),
		SfdcExternalID:              str(m, "sfdcExternalId"),
		AdditionalEmails:            str(m, "additionalEmails"),
		CrmCsm:                      str(m, "crmCsm"),
		TalkdeskRegion:              str(m, "talkdeskRegion"),
		CrmGrowthManager:            str(m, "crmGrowthManager"),
		TalkdeskIdPlatform:          str(m, "talkdeskIdPlatform"),
		ZuoraInvoiceName:            str(m, "zuoraInvoiceName"),
		EstimatedBudget:             str(m, "estimatedBudget"),
		BudgetApproved:              bval(m, "budgetApproved"),
		SalesReadiness:              str(m, "salesReadiness"),
		BuyingReason:                str(m, "buyingReason"),
		BuyingTimeFrame:             str(m, "buyingTimeFrame"),
		CustomFields:                mapVal(m, "customFields"),
	}
	if l.CustomForm == "" {
		l.CustomForm = "Standard Lead Form"
	}
	if l.LeadStatus == "" {
		l.LeadStatus = "LEAD-Unqualified"
	}
	if l.Type == "" {
		l.Type = "Company"
	}
	if l.CustomerType == "" {
		l.CustomerType = "Customer"
	}
	if l.CustomFields == nil {
		l.CustomFields = map[string]any{}
	}
	return l
}

const leadCols = `
	id, lead_id, COALESCE(owner_user_id::text,'') AS owner_user_id,
	custom_form, lead_status, default_order_priority, type,
	company_name, first_name, last_name,
	email, phone, fax, address,
	sales_rep, territory, partner,
	primary_subsidiary, email_for_payment_notification,
	white_glove, display_product_code, blackline_ar_cash_app,
	sfdc_account_id, prev_external_id, sfdc_customer_status,
	crm_account_owner, customer_legal_name, customer_type,
	crm_csm_team, sfdc_external_id, additional_emails,
	crm_csm, talkdesk_region, crm_growth_manager, talkdesk_id_platform,
	zuora_invoice_name, estimated_budget, budget_approved,
	sales_readiness, buying_reason, buying_time_frame,
	custom_fields,
	created_at, updated_at`

func scanLead(row pgx.Row) (*Lead, error) {
	var l Lead
	err := row.Scan(
		&l.ID, &l.LeadID, &l.OwnerUserID,
		&l.CustomForm, &l.LeadStatus, &l.DefaultOrderPriority, &l.Type,
		&l.CompanyName, &l.FirstName, &l.LastName,
		&l.Email, &l.Phone, &l.Fax, &l.Address,
		&l.SalesRep, &l.Territory, &l.Partner,
		&l.PrimarySubsidiary, &l.EmailForPaymentNotification,
		&l.WhiteGlove, &l.DisplayProductCode, &l.BlacklineArCashApp,
		&l.SfdcAccountID, &l.PrevExternalID, &l.SfdcCustomerStatus,
		&l.CrmAccountOwner, &l.CustomerLegalName, &l.CustomerType,
		&l.CrmCsmTeam, &l.SfdcExternalID, &l.AdditionalEmails,
		&l.CrmCsm, &l.TalkdeskRegion, &l.CrmGrowthManager, &l.TalkdeskIdPlatform,
		&l.ZuoraInvoiceName, &l.EstimatedBudget, &l.BudgetApproved,
		&l.SalesReadiness, &l.BuyingReason, &l.BuyingTimeFrame,
		&l.CustomFields,
		&l.CreatedAt, &l.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

// List returns leads in the tenant DB, filtered by the caller's RBAC scope:
//   - "all":  every lead
//   - "team": leads the caller owns OR assigned to one of the caller's teams
//   - "own":  leads the caller owns
func List(ctx context.Context, pool *pgxpool.Pool, scope, callerUserID string, teamIDs []string) ([]Lead, error) {
	var (
		rows pgx.Rows
		err  error
		base = `SELECT ` + leadCols + ` FROM leads`
	)
	switch scope {
	case "all":
		rows, err = pool.Query(ctx, base+` ORDER BY created_at DESC`)
	case "team":
		rows, err = pool.Query(ctx, base+`
			WHERE owner_user_id = $1 OR team_id = ANY($2) ORDER BY created_at DESC`,
			nullIfEmpty(callerUserID), teamIDs)
	default: // own (most restrictive)
		rows, err = pool.Query(ctx, base+`
			WHERE owner_user_id = $1 ORDER BY created_at DESC`, nullIfEmpty(callerUserID))
	}
	if err != nil {
		return nil, fmt.Errorf("list leads: %w", err)
	}
	defer rows.Close()
	var out []Lead
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// TeamIDsForUser lists the team ids a tenant user belongs to.
func TeamIDsForUser(ctx context.Context, pool *pgxpool.Pool, userID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT team_id FROM team_members WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("teams for user: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan team id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// nullIfEmpty returns nil for an empty string so it compares as SQL NULL
// (an empty owner filter then matches nothing rather than erroring on UUID cast).
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Get returns one lead by id.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Lead, error) {
	return scanLead(pool.QueryRow(ctx, `SELECT `+leadCols+` FROM leads WHERE id = $1`, id))
}

// Create inserts a new lead and returns it with generated id, lead_id, timestamps.
func Create(ctx context.Context, pool *pgxpool.Pool, l Lead) (*Lead, error) {
	ownerID := &l.OwnerUserID
	if l.OwnerUserID == "" {
		ownerID = nil
	}
	if l.CustomFields == nil {
		l.CustomFields = map[string]any{}
	}
	row := pool.QueryRow(ctx, `
		INSERT INTO leads (
			owner_user_id, custom_form, lead_status, default_order_priority, type,
			company_name, first_name, last_name, email, phone, fax, address,
			sales_rep, territory, partner, primary_subsidiary,
			email_for_payment_notification, white_glove, display_product_code,
			blackline_ar_cash_app, sfdc_account_id, prev_external_id,
			sfdc_customer_status, crm_account_owner, customer_legal_name,
			customer_type, crm_csm_team, sfdc_external_id, additional_emails,
			crm_csm, talkdesk_region, crm_growth_manager, talkdesk_id_platform,
			zuora_invoice_name, estimated_budget, budget_approved,
			sales_readiness, buying_reason, buying_time_frame,
			custom_fields
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
			$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40
		)
		RETURNING `+leadCols,
		ownerID, l.CustomForm, l.LeadStatus, l.DefaultOrderPriority, l.Type,
		l.CompanyName, l.FirstName, l.LastName, l.Email, l.Phone, l.Fax, l.Address,
		l.SalesRep, l.Territory, l.Partner, l.PrimarySubsidiary,
		l.EmailForPaymentNotification, l.WhiteGlove, l.DisplayProductCode,
		l.BlacklineArCashApp, l.SfdcAccountID, l.PrevExternalID,
		l.SfdcCustomerStatus, l.CrmAccountOwner, l.CustomerLegalName,
		l.CustomerType, l.CrmCsmTeam, l.SfdcExternalID, l.AdditionalEmails,
		l.CrmCsm, l.TalkdeskRegion, l.CrmGrowthManager, l.TalkdeskIdPlatform,
		l.ZuoraInvoiceName, l.EstimatedBudget, l.BudgetApproved,
		l.SalesReadiness, l.BuyingReason, l.BuyingTimeFrame,
		l.CustomFields,
	)
	return scanLead(row)
}

// Update patches a lead and returns the updated row.
func Update(ctx context.Context, pool *pgxpool.Pool, id string, l Lead) (*Lead, error) {
	ownerID := &l.OwnerUserID
	if l.OwnerUserID == "" {
		ownerID = nil
	}
	if l.CustomFields == nil {
		l.CustomFields = map[string]any{}
	}
	row := pool.QueryRow(ctx, `
		UPDATE leads SET
			owner_user_id=$1, custom_form=$2, lead_status=$3, default_order_priority=$4, type=$5,
			company_name=$6, first_name=$7, last_name=$8, email=$9, phone=$10, fax=$11, address=$12,
			sales_rep=$13, territory=$14, partner=$15, primary_subsidiary=$16,
			email_for_payment_notification=$17, white_glove=$18, display_product_code=$19,
			blackline_ar_cash_app=$20, sfdc_account_id=$21, prev_external_id=$22,
			sfdc_customer_status=$23, crm_account_owner=$24, customer_legal_name=$25,
			customer_type=$26, crm_csm_team=$27, sfdc_external_id=$28, additional_emails=$29,
			crm_csm=$30, talkdesk_region=$31, crm_growth_manager=$32, talkdesk_id_platform=$33,
			zuora_invoice_name=$34, estimated_budget=$35, budget_approved=$36,
			sales_readiness=$37, buying_reason=$38, buying_time_frame=$39,
			custom_fields=$40,
			updated_at=NOW()
		WHERE id=$41
		RETURNING `+leadCols,
		ownerID, l.CustomForm, l.LeadStatus, l.DefaultOrderPriority, l.Type,
		l.CompanyName, l.FirstName, l.LastName, l.Email, l.Phone, l.Fax, l.Address,
		l.SalesRep, l.Territory, l.Partner, l.PrimarySubsidiary,
		l.EmailForPaymentNotification, l.WhiteGlove, l.DisplayProductCode,
		l.BlacklineArCashApp, l.SfdcAccountID, l.PrevExternalID,
		l.SfdcCustomerStatus, l.CrmAccountOwner, l.CustomerLegalName,
		l.CustomerType, l.CrmCsmTeam, l.SfdcExternalID, l.AdditionalEmails,
		l.CrmCsm, l.TalkdeskRegion, l.CrmGrowthManager, l.TalkdeskIdPlatform,
		l.ZuoraInvoiceName, l.EstimatedBudget, l.BudgetApproved,
		l.SalesReadiness, l.BuyingReason, l.BuyingTimeFrame,
		l.CustomFields,
		id,
	)
	return scanLead(row)
}

// Delete removes a lead by id.
func Delete(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM leads WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete lead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
