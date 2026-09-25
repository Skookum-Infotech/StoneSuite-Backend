// crmstore/relational_status.go
//
// CRM status rules for the relational (v2) store, kept out of relational_store.go
// (already far past the 300-line cap). Three rules live here:
//
//   - The status a record STARTS in for each stage -- Lead New, Prospect New,
//     Customer Draft -- resolved by code, never by "lowest id": tenants that
//     predate these statuses were seeded with them at the highest ids.
//   - Which status moves are legal. Every stage has a fixed flow (crmStageFlows).
//     A lead goes New -> Qualified | Unqualified, both final. A prospect moves
//     freely between its working statuses, and from them to Lost or Pending
//     Conversion. A customer starts in Draft, becomes Active (by approval, or by
//     hand when nobody has to approve it), and is then put on Credit Hold or made
//     Inactive and back -- only an Active customer can be used on other records
//     (workflow/customer_usable.go).
//   - Which status a record must be in before it may be converted onward
//     (crmConvertRules): a Qualified lead, a Pending Conversion prospect.
//   - Which status approving or rejecting a record settles it in
//     (crmApprovedStatusCode, crmRejectedStatusCode): a customer becomes Active
//     when approved and goes back to Draft when rejected.
//
// Every rule is a pure function over status codes, so it is unit-testable
// without a database, and AvailableTransitions and TransitionRecord share ONE
// predicate (crmTransitionAllowed) -- the dropdown can never offer a move the
// API would refuse. The one deliberate gap runs the other way: an action-only
// status (crmActionOnlyStatuses) is accepted by the API but never listed. The
// approval gate (relational_approval.go) is a separate concern layered on top.
// The frontend mirrors crmConvertRules, the prospect's working statuses and the
// customer's buttons in src/lib/crmStatusFlow.ts -- keep them in sync.
package crmstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
	"stonesuite-backend/workflow"
)

// CRM status codes (lkp_crm_status.crm_status_code) that the rules below key on.
const (
	statusLeadNew         = "LNEW"
	statusLeadQualified   = "LQUA"
	statusLeadUnqualified = "LUNQ"

	statusProspectNew               = "PNEW"
	statusProspectInDiscussion      = "PDIS"
	statusProspectInNegotiation     = "PNEG"
	statusProspectProposalSent      = "PPRP"
	statusProspectDecisionPending   = "PIDM"
	statusProspectContacted         = "PPUR"
	statusProspectLost              = "PCLL"
	statusProspectPendingConversion = "PPCV"

	statusCustomerDraft      = "CDRF"
	statusCustomerActive     = workflow.CustomerStatusActive
	statusCustomerInactive   = "CINA"
	statusCustomerCreditHold = "CCHD"
)

// The 400s shown when a record that is not in its stage's convert-from status
// is converted.
const (
	msgConvertNeedsQualified         = "Only a Qualified lead can be converted."
	msgConvertNeedsPendingConversion = "Only a prospect in Pending Conversion can be converted."
)

// crmInitialStatusCode is the status a record takes on entering each stage
// (keyed by record type code), on create and on convert. It is entry-only: no
// transition may target it.
var crmInitialStatusCode = map[string]string{
	"LEAD": statusLeadNew,
	"PROS": statusProspectNew,
	"CUST": statusCustomerDraft,
}

// crmStageFlows holds the status graph of each stage, keyed by record type code.
// Lead's Qualified and Unqualified are deliberately final: a lead's outcome is
// settled once, and the way forward from Qualified is converting it into a
// prospect. A prospect's way forward is the same: it is marked Pending
// Conversion and then converted into a customer.
//
// A customer has no way forward -- it is the end of the pipeline -- so its flow
// is about whether it may be used: Draft leaves only by being made Active, an
// Active customer can be put on Credit Hold or made Inactive, and either of those
// can be made Active again. Nothing goes back to Draft except a rejection
// (crmRejectedStatusCode), which is not a transition.
var crmStageFlows = map[string]docflow.Machine{
	"LEAD": {
		statusLeadNew:         {statusLeadQualified: true, statusLeadUnqualified: true},
		statusLeadQualified:   {},
		statusLeadUnqualified: {},
	},
	"PROS": prospectFlow(),
	"CUST": {
		statusCustomerDraft:      {statusCustomerActive: true},
		statusCustomerActive:     {statusCustomerCreditHold: true, statusCustomerInactive: true},
		statusCustomerCreditHold: {statusCustomerActive: true, statusCustomerInactive: true},
		statusCustomerInactive:   {statusCustomerActive: true},
	},
}

// crmApprovedStatusCode and crmRejectedStatusCode are the status a record is
// settled in when its stage's approvers approve or reject it, keyed by record
// type code. A stage with no entry keeps whatever status the record has. Only a
// customer has one: approved, it is Active and usable; rejected, it returns to
// Draft (the rejection banner and reason stay, and editing it resubmits).
var (
	crmApprovedStatusCode = map[string]string{"CUST": statusCustomerActive}
	crmRejectedStatusCode = map[string]string{"CUST": statusCustomerDraft}
)

// prospectWorking are the statuses a prospect is worked through. It moves
// freely between them, and a prospect in any of them may be marked Pending
// Conversion. The frontend mirrors this list (CRM_PENDING_CONVERSION_FROM in
// src/lib/crmStatusFlow.ts) to know when to offer that button.
var prospectWorking = []string{
	statusProspectInDiscussion, statusProspectInNegotiation, statusProspectProposalSent,
	statusProspectDecisionPending, statusProspectContacted,
}

// prospectFlow builds the Prospect stage's status graph:
//
//   - New goes to any working status, or straight to Lost;
//   - a working status goes to any other working status, to Lost, or to Pending
//     Conversion;
//   - Lost goes back to any working status (a lost deal can be reopened) but never
//     to Pending Conversion -- a deal is worked before it is converted;
//   - Pending Conversion goes back to any working status or to Lost, so marking
//     one by mistake can be undone.
//
// Nothing goes back to New, and nothing leaves the stage: a customer is made by
// converting, not by moving the prospect itself.
func prospectFlow() docflow.Machine {
	lost := []string{statusProspectLost}
	flow := docflow.Machine{
		statusProspectNew:               targetSet(slices.Concat(prospectWorking, lost)...),
		statusProspectLost:              targetSet(prospectWorking...),
		statusProspectPendingConversion: targetSet(slices.Concat(prospectWorking, lost)...),
	}
	for _, from := range prospectWorking {
		others := slices.DeleteFunc(slices.Clone(prospectWorking), func(code string) bool { return code == from })
		flow[from] = targetSet(slices.Concat(others, lost, []string{statusProspectPendingConversion})...)
	}
	return flow
}

// targetSet turns a list of status codes into a docflow.Machine target set.
func targetSet(codes ...string) map[string]bool {
	set := make(map[string]bool, len(codes))
	for _, code := range codes {
		set[code] = true
	}
	return set
}

// crmEditedStatusCode is the status a record is put back in when someone edits
// it, keyed by record type code. A stage with no entry keeps its status through
// an edit. Only a customer has one: its status decides whether it can be used on
// other records, so an edit sends it back to Draft (unusable) until it is made
// Active again -- by its approvers, when the stage has any, else by hand.
var crmEditedStatusCode = map[string]string{"CUST": statusCustomerDraft}

// editedStatusReset returns the status an edit puts a record back in, or "" when
// it stays where it is: nothing was actually changed (saving an untouched form is
// not an edit), the stage has no such status, or the record is already in it.
// curStatusCode may be "" for a record with no status, which is put in it too.
func editedStatusReset(typeCode, curStatusCode string, changed bool) string {
	target := crmEditedStatusCode[typeCode]
	if !changed || target == "" || curStatusCode == target {
		return ""
	}
	return target
}

// crmFieldsChanged reports whether an update changes any stored field of a
// record: a registered core column, compared the way it is written to the
// database (writeArg) so that "" and a missing value, or 5 and "5", are not
// changes, or any custom field.
func crmFieldsChanged(beforeCore, afterCore, beforeCustom, afterCustom map[string]any) bool {
	for _, f := range customerFields {
		if writeArg(f, beforeCore) != writeArg(f, afterCore) {
			return true
		}
	}
	return !sameJSON(beforeCustom, afterCustom)
}

// sameJSON reports whether two maps serialise to the same JSON. encoding/json
// sorts map keys, so this is a deep comparison that ignores ordering, and it
// treats a nil map and an empty one as equal.
func sameJSON(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// crmActionOnlyStatuses are statuses a record reaches through a dedicated action
// (a button) rather than by picking them from the status dropdown: Pending
// Conversion, and the customer's Active, Inactive and Credit Hold (the Quick
// Action buttons Make Active, Make Inactive, Credit Hold and Release Hold). They
// stay legal targets for TransitionRecord -- the button goes through it -- but
// filterCRMTargets never lists them, so a customer's status shows as a plain pill.
var crmActionOnlyStatuses = map[string]bool{
	statusProspectPendingConversion: true,
	statusCustomerActive:            true,
	statusCustomerInactive:          true,
	statusCustomerCreditHold:        true,
}

// creditLockSQLSet returns the extra `, customer_is_credit_lock = ...` SQL
// fragment a same-stage TransitionRecord update should append, so the field
// defaults to matching whichever status the Credit Hold / Release Hold
// button just set: entering Credit Hold locks it, leaving it unlocks it.
// Empty for every other move, including every non-customer stage. This is a
// default, not an enforced lock: customer_is_credit_lock stays an ordinary,
// independently editable field on the Edit form afterward -- nothing stops
// it drifting from the status again from there. Mirrors the frontend's
// customer_is_credit_lock field (src/lib/crmFields.ts) and the buttons that
// set CCHD/CACT (CustomerStatusActions.tsx).
func creditLockSQLSet(typeCode, curStatusCode, targetStatusCode string) string {
	if typeCode != "CUST" {
		return ""
	}
	switch {
	case targetStatusCode == statusCustomerCreditHold:
		return `, customer_is_credit_lock = TRUE`
	case curStatusCode == statusCustomerCreditHold:
		return `, customer_is_credit_lock = FALSE`
	default:
		return ""
	}
}

// creditHoldStatusIDSQL resolves Credit Hold's status id for whichever
// customer row it is embedded next to -- the id creditLockRedirectSQL
// redirects a locked customer into instead of Active.
const creditHoldStatusIDSQL = `(SELECT cs.crm_status_id FROM lkp_crm_status cs
	WHERE cs.crm_status_record_type = customer.record_type
	  AND cs.crm_status_code = 'CCHD' AND cs.crm_status_is_active AND cs.crm_status_deleted_at IS NULL)`

// creditLockRedirectSQL wraps targetSQL -- a SQL expression that resolves to
// the status id a customer transition or approval is about to write -- so
// that landing on Active while customer_is_credit_lock is currently set
// redirects to Credit Hold instead. A customer created (or later edited)
// with the checkbox ticked can otherwise only ever reach Draft, since
// nothing stops it entering CUST there; without this, Make Active or an
// approval settling it would carry it straight into Active still flagged
// locked, contradicting the pill the moment it lands. Only a move that
// would land on Active is ever wrapped -- every other target (Make
// Inactive included) passes targetSQL through untouched regardless of the
// lock -- and curStatusCode excludes Release Hold itself (Credit Hold ->
// Active): that move clears the lock in the very same statement (see
// creditLockSQLSet), so wrapping it here would trap a customer on Credit
// Hold forever.
func creditLockRedirectSQL(typeCode, curStatusCode, targetStatusCode, targetSQL string) string {
	if typeCode != "CUST" || targetStatusCode != statusCustomerActive || curStatusCode == statusCustomerCreditHold {
		return targetSQL
	}
	return `CASE WHEN customer.customer_is_credit_lock THEN ` + creditHoldStatusIDSQL + ` ELSE (` + targetSQL + `) END`
}

// crmConvertRule says what a record must be in before it can be converted to a
// later stage, and what to tell the caller when it is not.
type crmConvertRule struct {
	from string // the status the record must be in
	msg  string // the 400 shown otherwise
}

// crmConvertRules is keyed by record type code. A stage with no entry converts
// from any status.
var crmConvertRules = map[string]crmConvertRule{
	"LEAD": {from: statusLeadQualified, msg: msgConvertNeedsQualified},
	"PROS": {from: statusProspectPendingConversion, msg: msgConvertNeedsPendingConversion},
}

// crmInitialStatusCodes lists every stage's initial status code, for queries
// that need to sort or flag them.
func crmInitialStatusCodes() []string {
	codes := make([]string, 0, len(crmInitialStatusCode))
	for _, code := range crmInitialStatusCode {
		codes = append(codes, code)
	}
	return codes
}

// crmTransitionAllowed reports whether a record in status curStatus of stage
// curType may move to targetStatus of stage targetType. curStatus may be "" (a
// record with no status); a stage with a flow then treats it as the stage's
// initial status.
//
// Every stage follows its flow exactly and never crosses into another stage:
// leaving the stage is what converting is for. A stage with no flow allows
// nothing.
func crmTransitionAllowed(curType, curStatus, targetType, targetStatus string) bool {
	flow, ok := crmStageFlows[curType]
	if !ok || targetType != curType {
		return false
	}
	from := curStatus
	if from == "" {
		from = crmInitialStatusCode[curType]
	}
	return flow.Can(from, targetStatus)
}

// filterCRMTargets narrows candidate statuses to the moves crmTransitionAllowed
// permits from the record's current stage and status, minus the action-only
// statuses (crmActionOnlyStatuses), which the dropdown never lists. Never
// returns nil, so it serialises as [] rather than null.
func filterCRMTargets(curType, curStatus string, candidates []workflow.StatusInfo) []workflow.StatusInfo {
	out := make([]workflow.StatusInfo, 0, len(candidates))
	for _, c := range candidates {
		if crmActionOnlyStatuses[c.StateKey] {
			continue
		}
		if crmTransitionAllowed(curType, curStatus, crmKeyToCode[c.WorkflowKey], c.StateKey) {
			out = append(out, c)
		}
	}
	return out
}

// crmStatusIsTerminal reports whether a status is an end point the UI should
// confirm before applying: a "Closed ..." status (the original name-based
// rule), a lost prospect (Lost no longer says "closed" in its name, and unlike
// the rest it is not a dead end in its flow, since it can be reopened), or one
// its stage's flow gives no way out of.
func crmStatusIsTerminal(typeCode, statusCode, statusName string) bool {
	if statusCode == statusProspectLost || strings.Contains(strings.ToLower(statusName), "closed") {
		return true
	}
	flow, ok := crmStageFlows[typeCode]
	return ok && flow.Known(statusCode) && flow.IsTerminal(statusCode)
}

// markInitialStatuses flags each stage's initial status (crmInitialStatusCode)
// on a status list.
func markInitialStatuses(statuses []workflow.StatusInfo) []workflow.StatusInfo {
	for i := range statuses {
		s := &statuses[i]
		s.IsInitial = s.StateKey == crmInitialStatusCode[crmKeyToCode[s.WorkflowKey]]
	}
	return statuses
}

// checkConvertFrom enforces crmConvertRules: a record whose stage requires a
// particular status before converting (a Lead must be Qualified, a Prospect
// Pending Conversion) is refused with a 400 otherwise. statusCode may be "" for
// a record with no status.
func checkConvertFrom(typeCode, statusCode string) error {
	rule, constrained := crmConvertRules[typeCode]
	if !constrained || statusCode == rule.from {
		return nil
	}
	return ClientError{Msg: rule.msg}
}

// initialStatusID resolves the initial status of the stage whose record type id
// is typeID. It is what CreateRecord and ConvertRecord give a new record: the
// entry status is decided here, never chosen by the caller.
func (s *relationalStore) initialStatusID(ctx context.Context, pool *pgxpool.Pool, typeID int, typeCode string) (int, error) {
	code, ok := crmInitialStatusCode[typeCode]
	if !ok {
		return 0, fmt.Errorf("no initial CRM status is defined for record type %q", typeCode)
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT crm_status_id FROM lkp_crm_status
		WHERE crm_status_record_type = $1 AND crm_status_code = $2
		  AND crm_status_is_active AND crm_status_deleted_at IS NULL`,
		typeID, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("initial status %q for record type %q: %w", code, typeCode, err)
	}
	return id, nil
}

// settledStatusSQL is the SQL expression for customer.customer_crm_status in the
// UPDATE that approves or rejects a record: the status of the record's own stage
// whose code is bound to codeParam ("$3"), or the record's current status when
// that code is "" -- a stage approval does not move, or one whose status row is
// missing. Doing it in the same statement keeps the approval decision and the
// status change atomic.
func settledStatusSQL(codeParam string) string {
	return `COALESCE((SELECT cs.crm_status_id FROM lkp_crm_status cs
			WHERE cs.crm_status_record_type = customer.record_type
			  AND cs.crm_status_code = ` + codeParam + `
			  AND cs.crm_status_is_active AND cs.crm_status_deleted_at IS NULL), customer_crm_status)`
}

// statusCodeByID returns the code of a status, or "" for statusID 0 (a record
// with no status).
func (s *relationalStore) statusCodeByID(ctx context.Context, pool *pgxpool.Pool, statusID int) (string, error) {
	if statusID == 0 {
		return "", nil
	}
	_, code, err := s.statusTypeAndCode(ctx, pool, statusID)
	return code, err
}

// findConvertedChild returns the uuid of a live record already converted from
// the record with internal id parentID that sits at targetCode's stage or later
// -- a prospect that has since advanced into Customers still counts as "the
// prospect" made from the lead. found is false when there is none.
func (s *relationalStore) findConvertedChild(ctx context.Context, pool *pgxpool.Pool, parentID int, targetCode string) (uuid string, found bool, err error) {
	err = pool.QueryRow(ctx, `
		SELECT c.customer_uuid
		FROM customer c JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		WHERE c.customer_parent_id = $1 AND c.customer_deleted_at IS NULL
		  AND rt.record_type_code = ANY($2)
		ORDER BY c.customer_created_at, c.customer_id
		LIMIT 1`,
		parentID, reachableCRMCodes(crmCodeRank[targetCode])).Scan(&uuid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find converted record: %w", err)
	}
	return uuid, true, nil
}
