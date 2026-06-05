package lead

import "time"

// Lead mirrors the leads table row.
type Lead struct {
	ID                        string    `json:"id"`
	LeadID                    string    `json:"leadId"`
	OwnerUserID               string    `json:"ownerUserId,omitempty"`
	CustomForm                string    `json:"customForm"`
	LeadStatus                string    `json:"leadStatus"`
	DefaultOrderPriority      string    `json:"defaultOrderPriority,omitempty"`
	Type                      string    `json:"type"`
	CompanyName               string    `json:"companyName,omitempty"`
	FirstName                 string    `json:"firstName,omitempty"`
	LastName                  string    `json:"lastName,omitempty"`
	Email                     string    `json:"email,omitempty"`
	Phone                     string    `json:"phone,omitempty"`
	Fax                       string    `json:"fax,omitempty"`
	Address                   string    `json:"address,omitempty"`
	SalesRep                  string    `json:"salesRep,omitempty"`
	Territory                 string    `json:"territory,omitempty"`
	Partner                   string    `json:"partner,omitempty"`
	PrimarySubsidiary         string    `json:"primarySubsidiary"`
	EmailForPaymentNotification string  `json:"emailForPaymentNotification,omitempty"`
	WhiteGlove                bool      `json:"whiteGlove"`
	DisplayProductCode        bool      `json:"displayProductCode"`
	BlacklineArCashApp        bool      `json:"blacklineArCashApp"`
	SfdcAccountID             string    `json:"sfdcAccountId,omitempty"`
	PrevExternalID            string    `json:"prevExternalId,omitempty"`
	SfdcCustomerStatus        string    `json:"sfdcCustomerStatus,omitempty"`
	CrmAccountOwner           string    `json:"crmAccountOwner,omitempty"`
	CustomerLegalName         string    `json:"customerLegalName,omitempty"`
	CustomerType              string    `json:"customerType,omitempty"`
	CrmCsmTeam                string    `json:"crmCsmTeam,omitempty"`
	SfdcExternalID            string    `json:"sfdcExternalId,omitempty"`
	AdditionalEmails          string    `json:"additionalEmails,omitempty"`
	CrmCsm                    string    `json:"crmCsm,omitempty"`
	TalkdeskRegion            string    `json:"talkdeskRegion,omitempty"`
	CrmGrowthManager          string    `json:"crmGrowthManager,omitempty"`
	TalkdeskIdPlatform        string    `json:"talkdeskIdPlatform,omitempty"`
	ZuoraInvoiceName          string    `json:"zuoraInvoiceName,omitempty"`
	EstimatedBudget           string    `json:"estimatedBudget,omitempty"`
	BudgetApproved            bool      `json:"budgetApproved"`
	SalesReadiness            string    `json:"salesReadiness,omitempty"`
	BuyingReason              string    `json:"buyingReason,omitempty"`
	BuyingTimeFrame           string    `json:"buyingTimeFrame,omitempty"`
	CustomFields              map[string]any `json:"customFields,omitempty"`
	CreatedAt                 time.Time      `json:"createdAt"`
	UpdatedAt                 time.Time      `json:"updatedAt"`
}
