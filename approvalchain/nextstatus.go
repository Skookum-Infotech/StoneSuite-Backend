// approvalchain/nextstatus.go
package approvalchain

import (
	"context"
	"sort"

	"stonesuite-backend/workflow"
)

// UngatedGates reports which of cfg.Gates currently have nobody configured to
// approve them -- neither on the gate status itself nor on its target (a
// target that is itself a live checkpoint, as in fabrication's chained
// gates, still has to be stopped at). Keyed by the gate's StatusCode.
//
// This is two count queries per gate, so a caller serving a list should call
// it once per request and reuse the result for every row (see
// NextStatusCodes), not once per record.
func UngatedGates(ctx context.Context, q workflow.Querier, cfg ModuleConfig, recordTypeID int) (map[string]Gate, error) {
	countAt := func(code string) (int, error) {
		statusID, _, err := statusIDAndLabelByCode(ctx, q, recordTypeID, code)
		if err != nil {
			return 0, err
		}
		return activeApproverCount(ctx, q, cfg.ApproverTable, recordTypeID, statusID)
	}
	out := map[string]Gate{}
	for _, g := range cfg.Gates {
		atGate, err := countAt(g.StatusCode)
		if err != nil {
			return nil, err
		}
		atTarget, err := countAt(g.TargetStatusCode)
		if err != nil {
			return nil, err
		}
		if atGate == 0 && atTarget == 0 {
			out[g.StatusCode] = g
		}
	}
	return out, nil
}

// NextStatusCodes lists the status codes a record at fromCode may be moved to
// right now, for the UI's status control and for Transition's own validation
// (PassThroughGate) so the two can never disagree. next returns a module's
// static next-moves for a status (its transitions.go map); ungated is
// UngatedGates' result.
//
// Every static next-move is offered as-is, except a gate nobody is configured
// to approve: that checkpoint is collapsed out (there is nothing to wait for
// there) and replaced by either its target -- the record can be approved in
// one step -- or, when the gate's SkipTargetWhenUngated is set, the target's
// own next-moves, so a Draft with no approver goes straight to Sent without
// ever visiting Approved. A target with nowhere further to go is kept as-is
// regardless, so a record is never stranded. One level only: a status reached
// by collapsing a gate is offered even if it is itself a gate.
//
// Sorted, deduplicated, and never including fromCode itself.
func NextStatusCodes(fromCode string, next func(string) []string, ungated map[string]Gate) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(code string) {
		if code == fromCode || seen[code] {
			return
		}
		seen[code] = true
		out = append(out, code)
	}
	for _, code := range next(fromCode) {
		gate, collapse := ungated[code]
		if !collapse {
			add(code)
			continue
		}
		for _, code := range collapsedGateTargets(fromCode, gate, next) {
			add(code)
		}
	}
	sort.Strings(out)
	return out
}

// PassThroughGate reports whether a move fromCode->toCode that the module's
// static map rejects is nonetheless legal because it passes through an
// ungated checkpoint (exactly the moves NextStatusCodes offers beyond the
// static ones), and returns that gate so the caller can treat landing on the
// gate's target as an approval outcome.
func PassThroughGate(fromCode, toCode string, next func(string) []string, ungated map[string]Gate) (Gate, bool) {
	if toCode == fromCode {
		return Gate{}, false
	}
	for _, code := range next(fromCode) {
		gate, collapse := ungated[code]
		if !collapse {
			continue
		}
		for _, reachable := range collapsedGateTargets(fromCode, gate, next) {
			if reachable == toCode {
				return gate, true
			}
		}
	}
	return Gate{}, false
}

// collapsedGateTargets is what an ungated gate reachable from fromCode stands
// in for: its target, or -- with SkipTargetWhenUngated -- the target's own
// onward moves (minus a move back to fromCode, which is no move at all). A
// target whose only moves are back or out (Cancel/Void/Reject) is a resting
// state, not a waypoint, so it is kept as-is even when flagged to be skipped
// rather than leaving the record with nowhere forward to go.
func collapsedGateTargets(fromCode string, gate Gate, next func(string) []string) []string {
	if !gate.SkipTargetWhenUngated {
		return []string{gate.TargetStatusCode}
	}
	var onward []string
	forward := false
	for _, code := range next(gate.TargetStatusCode) {
		if code == fromCode {
			continue
		}
		onward = append(onward, code)
		if !AlwaysAllowedExitCodes[code] {
			forward = true
		}
	}
	if !forward {
		return []string{gate.TargetStatusCode}
	}
	return onward
}
