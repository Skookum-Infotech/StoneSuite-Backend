// crmstore/relational_status.go
//
// CRM status rules for the relational (v2) store, kept out of relational_store.go
// (already far past the 300-line cap). Three rules live here:
//
//   - The status a record STARTS in for each stage -- Lead New, Prospect New,
//     Customer Draft -- resolved by code, never by "lowest id": tenants that
//     predate these statuses were seeded with them at the highest ids.
//   - Which status moves are legal. Lead and Prospect each have a fixed flow
//     (crmStageFlows). A lead goes New -> Qualified | Unqualified, both final. A
//     prospect moves freely between its working statuses, and from them to Lost or
//     Pending Conversion. Customer keeps the original free-form rule: any status
//     of its own stage except the current one and the entry status.
//   - Which status a record must be in before it may be converted onward
//     (crmConvertRules): a Qualified lead, a Pending Conversion prospect.
//
// Every rule is a pure function over status codes, so it is unit-testable
// without a database, and AvailableTransitions and TransitionRecord share ONE
// predicate (crmTransitionAllowed) -- the dropdown can never offer a move the
// API would refuse. The one deliberate gap runs the other way: an action-only
// status (crmActionOnlyStatuses) is accepted by the API but never listed. The
// approval gate (relational_approval.go) is a separate concern layered on top
// and is unchanged. The frontend mirrors crmConvertRules and the prospect's
// working statuses in src/lib/crmStatusFlow.ts -- keep them in sync.
package crmstore

import (
	"context"
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

	statusCustomerDraft = "CDRF"
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

// crmStageFlows holds the status graph of each stage that has a fixed one,
// keyed by record type code. A stage with no entry keeps the free-form rule
// (see crmTransitionAllowed). Lead's Qualified and Unqualified are deliberately
// final: a lead's outcome is settled once, and the way forward from Qualified
// is converting it into a prospect. A prospect's way forward is the same: it is
// marked Pending Conversion and then converted into a customer.
var crmStageFlows = map[string]docflow.Machine{
	"LEAD": {
		statusLeadNew:         {statusLeadQualified: true, statusLeadUnqualified: true},
		statusLeadQualified:   {},
		statusLeadUnqualified: {},
	},
	"PROS": prospectFlow(),
}

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

// crmActionOnlyStatuses are statuses a record reaches through a dedicated action
// (a header button) rather than by picking them from the status dropdown. They
// stay legal targets for TransitionRecord -- the button goes through it -- but
// filterCRMTargets never lists them.
var crmActionOnlyStatuses = map[string]bool{statusProspectPendingConversion: true}

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
// A stage with a flow (Lead, Prospect) follows it exactly and never crosses
// into another stage: leaving the stage is what converting is for. Every other
// stage (Customer) keeps the original rule -- any status of the same or a later
// stage, except the record's current status and the entry-only initial
// statuses, so a record moves sideways but never back to "Draft".
func crmTransitionAllowed(curType, curStatus, targetType, targetStatus string) bool {
	if crmCodeRank[targetType] < crmCodeRank[curType] {
		return false // forward-only across stages
	}
	if flow, ok := crmStageFlows[curType]; ok {
		from := curStatus
		if from == "" {
			from = crmInitialStatusCode[curType]
		}
		return targetType == curType && flow.Can(from, targetStatus)
	}
	if targetType == curType && targetStatus == curStatus {
		return false
	}
	return targetStatus != crmInitialStatusCode[targetType]
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
