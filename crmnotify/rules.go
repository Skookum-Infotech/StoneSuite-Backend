// Package crmnotify sends the CRM lifecycle emails (Lead → Prospect → Customer):
// who is told when a record enters a status, is submitted for approval, or is
// rejected. It is dependency-light on purpose — it reads recipients with plain
// queries and sends through services.SendNotification — so controllers stay thin.
package crmnotify

import "stonesuite-backend/services"

// Group is a class of recipients an event is addressed to.
type Group string

// Recipient groups. Owner, Approvers and Submitter are resolved from the record;
// Manager and Finance are resolved from the tenant's crm_notify_recipient_role config.
const (
	GroupOwner     Group = "owner"
	GroupManager   Group = "manager"
	GroupFinance   Group = "finance"
	GroupApprovers Group = "approvers"
	GroupSubmitter Group = "submitter"
)

// Workflow keys of the three CRM stages.
const (
	KeyLead     = "lead"
	KeyProspect = "prospect"
	KeyCustomer = "customer"
)

// CRM status codes (lkp_crm_status.crm_status_code) the rules key on. They
// mirror crmstore/relational_status.go.
const (
	statusLeadNew                   = "LNEW"
	statusLeadQualified             = "LQUA"
	statusLeadUnqualified           = "LUNQ"
	statusProspectNew               = "PNEW"
	statusProspectNegotiation       = "PNEG"
	statusProspectProposalSent      = "PPRP"
	statusProspectDecisionPending   = "PIDM"
	statusProspectLost              = "PCLL"
	statusProspectPendingConversion = "PPCV"
	statusCustomerDraft             = "CDRF"
	statusCustomerActive            = "CACT"
	statusCustomerCreditHold        = "CCHD"
	statusCustomerInactive          = "CINA"
)

// Event types for the two events that are not a plain status entry.
const (
	eventCustomerApprovalRequested = "customer.approval_requested"
	eventCustomerApprovalRejected  = "customer.approval_rejected"
)

// note is the wording of one lifecycle email. Lead is the opening sentence with
// %s standing for the record label ("Lead LD-000012").
type note struct {
	Badge    string
	Verb     string // banner heading line 2 and title tail: "is now Qualified"
	Subtitle string
	Lead     string
	Icon     services.EmailIcon
	Reason   string // footer reason; the group-specific default is used when empty
}

// Rule says who is emailed, with what wording, when a record of one stage enters
// one status.
type Rule struct {
	EventType string
	Groups    []Group
	Note      note
}

type ruleKey struct{ Workflow, Status string }

// statusRules is the CRM email diagram as data. Contacted and In Discussion send
// nothing; every other status the diagram marks with an email appears here.
var statusRules = map[ruleKey]Rule{
	{KeyLead, statusLeadNew}: {"lead.created", []Group{GroupOwner},
		note{"Lead Created", "was created.", "A new lead is ready.", "%s was created successfully. You are the lead owner.", services.IconDocPlus, ""}},
	{KeyLead, statusLeadQualified}: {"lead.qualified", []Group{GroupOwner},
		note{"Lead Qualified", "is now Qualified.", "Ready to convert.", "%s has been marked Qualified. You can now convert it to a prospect.", services.IconDocCheck, ""}},
	{KeyLead, statusLeadUnqualified}: {"lead.unqualified", []Group{GroupManager},
		note{"Lead Unqualified", "is now Unqualified.", "No further action needed.", "%s has been marked Unqualified.", services.IconDocArrow, ""}},

	{KeyProspect, statusProspectNew}: {"prospect.created", []Group{GroupOwner, GroupManager},
		note{"Prospect Created", "was created.", "A new prospect is ready.", "%s was created and is ready to be worked.", services.IconDocPlus, ""}},
	{KeyProspect, statusProspectProposalSent}: {"prospect.proposal_sent", []Group{GroupManager},
		note{"Proposal Sent", "is now Proposal Sent.", "A proposal is with the prospect.", "A proposal has been sent for %s.", services.IconDocArrow, ""}},
	{KeyProspect, statusProspectNegotiation}: {"prospect.in_negotiation", []Group{GroupManager},
		note{"In Negotiation", "is now In Negotiation.", "Terms are being discussed.", "%s has moved into negotiation.", services.IconDocClock, ""}},
	{KeyProspect, statusProspectDecisionPending}: {"prospect.decision_pending", []Group{GroupOwner, GroupManager},
		note{"Decision Pending", "is now Decision Pending.", "Waiting on the prospect's decision.", "%s is waiting on the prospect's decision.", services.IconDocClock, ""}},
	{KeyProspect, statusProspectLost}: {"prospect.lost", []Group{GroupManager},
		note{"Prospect Lost", "is now Lost.", "This deal did not close.", "%s has been marked Lost.", services.IconDocArrow, ""}},
	{KeyProspect, statusProspectPendingConversion}: {"prospect.pending_conversion", []Group{GroupOwner},
		note{"Pending Conversion", "is ready to convert.", "Convert it to a customer.", "%s is in Pending Conversion. You can now convert it to a customer.", services.IconDocCheck, ""}},

	{KeyCustomer, statusCustomerDraft}: {"customer.draft", []Group{GroupOwner, GroupManager},
		note{"Customer Draft", "was created as a draft.", "Not yet usable on records.", "%s was created as a draft. It becomes usable once it is active.", services.IconDocPlus, ""}},
	{KeyCustomer, statusCustomerActive}: {"customer.active", []Group{GroupOwner, GroupManager, GroupFinance},
		note{"Customer Active", "is now Active.", "Usable on records.", "%s is now active and can be used on records.", services.IconDocCheck, ""}},
	{KeyCustomer, statusCustomerCreditHold}: {"customer.credit_hold", []Group{GroupOwner, GroupManager, GroupFinance},
		note{"Credit Hold", "is on Credit Hold.", "Credit hold is checked.", "%s has been placed on credit hold.", services.IconDocClock, ""}},
	{KeyCustomer, statusCustomerInactive}: {"customer.inactive", []Group{GroupOwner, GroupManager, GroupFinance},
		note{"Customer Inactive", "is now Inactive.", "No longer usable on records.", "%s is now inactive and can no longer be used on records.", services.IconDocArrow, ""}},
}

// approvalRequestedRule is sent to the configured approvers when a customer
// lands in Draft awaiting approval (created, converted, or edited after a rejection).
var approvalRequestedRule = Rule{eventCustomerApprovalRequested, []Group{GroupApprovers},
	note{"Approval Needed", "needs your approval.", "Please review and take action.",
		"%s has been submitted for your approval. Review the details, then approve or reject it.",
		services.IconDocClock, "you're an approver."}}

// approvalRejectedRule is sent to the submitter when an approver rejects the
// customer and it goes back to Draft.
var approvalRejectedRule = Rule{eventCustomerApprovalRejected, []Group{GroupSubmitter},
	note{"Sent Back", "was sent back.", "Please review and resubmit.",
		"%s was rejected by an approver and returned to Draft. Open the record to review it and resubmit.",
		services.IconDocArrow, "you submitted this record."}}

// RuleForStatus returns the email rule for a record of workflowKey entering
// statusCode, and false when that status sends no email.
func RuleForStatus(workflowKey, statusCode string) (Rule, bool) {
	r, ok := statusRules[ruleKey{workflowKey, statusCode}]
	return r, ok
}
