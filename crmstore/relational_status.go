// crmstore/relational_status.go
//
// CRM status rules for the relational (v2) store, kept out of relational_store.go
// (already far past the 300-line cap). Three rules live here:
//
//   - The status a record STARTS in for each stage -- Lead New, Prospect New,
//     Customer Draft -- resolved by code, never by "lowest id": tenants that
//     predate these statuses were seeded with them at the highest ids.
//   - Which status moves are legal. Only the Lead stage has a fixed flow (New ->
//     Qualified | Unqualified, both final); Prospect and Customer keep the
//     original free-form rule: any status of the record's own stage or a later
//     one.
//   - Which status a record must be in before it may be converted onward.
//
// Every rule is a pure function over status codes, so it is unit-testable
// without a database, and AvailableTransitions and TransitionRecord share ONE
// predicate (crmTransitionAllowed) -- the dropdown can never offer a move the
// API would refuse. The approval gate (relational_approval.go) is a separate
// concern layered on top and is unchanged. The frontend mirrors
// crmConvertFromStatus in src/lib/crmStatusFlow.ts -- keep the two in sync.
package crmstore

import (
	"context"
	"errors"
	"fmt"
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
	statusProspectNew     = "PNEW"
	statusCustomerDraft   = "CDRF"
)

// msgConvertNeedsQualified is the 400 shown when a lead that is not Qualified
// is converted.
const msgConvertNeedsQualified = "Only a Qualified lead can be converted."

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
// is converting it into a prospect.
var crmStageFlows = map[string]docflow.Machine{
	"LEAD": {
		statusLeadNew:         {statusLeadQualified: true, statusLeadUnqualified: true},
		statusLeadQualified:   {},
		statusLeadUnqualified: {},
	},
}

// crmConvertFromStatus names the status a record must be in before it can be
// converted to a later stage. A stage with no entry converts from any status.
var crmConvertFromStatus = map[string]string{"LEAD": statusLeadQualified}

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
// A stage with a flow follows it exactly and never crosses into another stage:
// leaving the stage is what converting is for. Every other stage keeps the
// original rule -- any status of the same or a later stage, except the
// record's current status and the entry-only initial statuses, so a record
// moves forward or sideways but never back to "New" or "Draft".
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
// permits from the record's current stage and status. Never returns nil, so it
// serialises as [] rather than null.
func filterCRMTargets(curType, curStatus string, candidates []workflow.StatusInfo) []workflow.StatusInfo {
	out := make([]workflow.StatusInfo, 0, len(candidates))
	for _, c := range candidates {
		if crmTransitionAllowed(curType, curStatus, crmKeyToCode[c.WorkflowKey], c.StateKey) {
			out = append(out, c)
		}
	}
	return out
}

// crmStatusIsTerminal reports whether a status is an end point the UI should
// confirm before applying: a "Closed ..." status (the original name-based
// rule) or one its stage's flow gives no way out of.
func crmStatusIsTerminal(typeCode, statusCode, statusName string) bool {
	if strings.Contains(strings.ToLower(statusName), "closed") {
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

// checkConvertFrom enforces crmConvertFromStatus: a record whose stage requires
// a particular status before converting (a Lead must be Qualified) is refused
// with a 400 otherwise. statusCode may be "" for a record with no status.
func checkConvertFrom(typeCode, statusCode string) error {
	required, constrained := crmConvertFromStatus[typeCode]
	if !constrained || statusCode == required {
		return nil
	}
	return ClientError{Msg: msgConvertNeedsQualified}
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
