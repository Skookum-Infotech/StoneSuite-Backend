package models

import "time"

type Lead struct {
	ID                          string    `json:"id"`
	LeadID                      string    `json:"leadId"`
	CustomForm                  string    `json:"customForm"`
	LeadStatus                  string    `json:"leadStatus"`
	DefaultOrderPriority        string    `json:"defaultOrderPriority,omitempty"`
	Type                        string    `json:"type"`
	CompanyName                 string    `json:"companyName,omitempty"`
	FirstName                   string    `json:"firstName,omitempty"`
	LastName                    string    `json:"lastName,omitempty"`
	SalesRep                    string    `json:"salesRep,omitempty"`
	Territory                   string    `json:"territory,omitempty"`
	Partner                     string    `json:"partner,omitempty"`
	Email                       string    `json:"email,omitempty"`
	Phone                       string    `json:"phone,omitempty"`
	Fax                         string    `json:"fax,omitempty"`
	Address                     string    `json:"address,omitempty"`
	PrimarySubsidiary           string    `json:"primarySubsidiary"`
	EmailForPaymentNotification string    `json:"emailForPaymentNotification,omitempty"`
	WhiteGlove                  bool      `json:"whiteGlove"`
	DisplayProductCode          bool      `json:"displayProductCode"`
	BlacklineArCashApp          bool      `json:"blacklineArCashApp"`
	SfdcAccountID               string    `json:"sfdcAccountId,omitempty"`
	PrevExternalID              string    `json:"prevExternalId,omitempty"`
	SfdcCustomerStatus          string    `json:"sfdcCustomerStatus,omitempty"`
	CrmAccountOwner             string    `json:"crmAccountOwner,omitempty"`
	CustomerLegalName           string    `json:"customerLegalName,omitempty"`
	CustomerType                string    `json:"customerType,omitempty"`
	CrmCsmTeam                  string    `json:"crmCsmTeam,omitempty"`
	SfdcExternalID              string    `json:"sfdcExternalId,omitempty"`
	AdditionalEmails            string    `json:"additionalEmails,omitempty"`
	CrmCsm                      string    `json:"crmCsm,omitempty"`
	TalkdeskRegion              string    `json:"talkdeskRegion,omitempty"`
	CrmGrowthManager            string    `json:"crmGrowthManager,omitempty"`
	TalkdeskIdPlatform          string    `json:"talkdeskIdPlatform,omitempty"`
	ZuoraInvoiceName            string    `json:"zuoraInvoiceName,omitempty"`
	EstimatedBudget             string    `json:"estimatedBudget,omitempty"`
	BudgetApproved              bool      `json:"budgetApproved"`
	SalesReadiness              string    `json:"salesReadiness,omitempty"`
	BuyingReason                string    `json:"buyingReason,omitempty"`
	BuyingTimeFrame             string    `json:"buyingTimeFrame,omitempty"`
	CreatedAt                   time.Time `json:"createdAt"`
	UpdatedAt                   time.Time `json:"updatedAt"`
}

type CreateLeadRequest struct {
	CustomForm                  string `json:"customForm"`
	LeadStatus                  string `json:"leadStatus"`
	DefaultOrderPriority        string `json:"defaultOrderPriority"`
	Type                        string `json:"type"`
	CompanyName                 string `json:"companyName"`
	FirstName                   string `json:"firstName"`
	LastName                    string `json:"lastName"`
	SalesRep                    string `json:"salesRep"`
	Territory                   string `json:"territory"`
	Partner                     string `json:"partner"`
	Email                       string `json:"email"`
	Phone                       string `json:"phone"`
	Fax                         string `json:"fax"`
	Address                     string `json:"address"`
	PrimarySubsidiary           string `json:"primarySubsidiary"`
	EmailForPaymentNotification string `json:"emailForPaymentNotification"`
	WhiteGlove                  bool   `json:"whiteGlove"`
	DisplayProductCode          bool   `json:"displayProductCode"`
	BlacklineArCashApp          bool   `json:"blacklineArCashApp"`
	SfdcAccountID               string `json:"sfdcAccountId"`
	PrevExternalID              string `json:"prevExternalId"`
	SfdcCustomerStatus          string `json:"sfdcCustomerStatus"`
	CrmAccountOwner             string `json:"crmAccountOwner"`
	CustomerLegalName           string `json:"customerLegalName"`
	CustomerType                string `json:"customerType"`
	CrmCsmTeam                  string `json:"crmCsmTeam"`
	SfdcExternalID              string `json:"sfdcExternalId"`
	AdditionalEmails            string `json:"additionalEmails"`
	CrmCsm                      string `json:"crmCsm"`
	TalkdeskRegion              string `json:"talkdeskRegion"`
	CrmGrowthManager            string `json:"crmGrowthManager"`
	TalkdeskIdPlatform          string `json:"talkdeskIdPlatform"`
	ZuoraInvoiceName            string `json:"zuoraInvoiceName"`
	EstimatedBudget             string `json:"estimatedBudget"`
	BudgetApproved              bool   `json:"budgetApproved"`
	SalesReadiness              string `json:"salesReadiness"`
	BuyingReason                string `json:"buyingReason"`
	BuyingTimeFrame             string `json:"buyingTimeFrame"`
}
