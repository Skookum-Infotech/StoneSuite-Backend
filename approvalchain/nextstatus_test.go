package approvalchain

import (
	"reflect"
	"testing"
)

// A document-style state machine (estimate/quote/invoice-shaped): Draft
// submits to a PAPV checkpoint whose target Approved then leads on to Sent.
var docNext = func(code string) []string {
	return map[string][]string{
		"DRFT": {"PAPV", "CANC"},
		"PAPV": {"APPV", "DRFT", "CANC"},
		"APPV": {"SENT", "DRFT", "CANC"},
		"SENT": {"CANC"},
		"CANC": {},
	}[code]
}

// A requisition-shaped machine: Approved is where a record rests (its only
// moves are back to Draft or Cancel), so it must stay reachable.
var restingNext = func(code string) []string {
	return map[string][]string{
		"DRFT": {"PAPV", "CANC"},
		"PAPV": {"APPV", "DRFT", "CANC"},
		"APPV": {"DRFT", "CANC"},
		"CANC": {},
	}[code]
}

func TestNextStatusCodes(t *testing.T) {
	skip := Gate{StatusCode: "PAPV", TargetStatusCode: "APPV", SkipTargetWhenUngated: true}
	keep := Gate{StatusCode: "PAPV", TargetStatusCode: "APPV"}

	tests := []struct {
		name    string
		from    string
		next    func(string) []string
		ungated map[string]Gate
		want    []string
	}{
		{"gate configured: static moves untouched", "DRFT", docNext, nil, []string{"CANC", "PAPV"}},
		{"ungated waypoint gate: Draft goes straight past Approved", "DRFT", docNext, map[string]Gate{"PAPV": skip}, []string{"CANC", "SENT"}},
		{"ungated resting gate: Draft is offered Approved itself", "DRFT", restingNext, map[string]Gate{"PAPV": keep}, []string{"APPV", "CANC"}},
		{"ungated waypoint whose target has nowhere onward keeps the target", "DRFT", restingNext, map[string]Gate{"PAPV": skip}, []string{"APPV", "CANC"}},
		{"statuses past the gate are unaffected", "APPV", docNext, map[string]Gate{"PAPV": skip}, []string{"CANC", "DRFT", "SENT"}},
		{"sitting on the ungated gate itself: its own static moves", "PAPV", docNext, map[string]Gate{"PAPV": skip}, []string{"APPV", "CANC", "DRFT"}},
		{"terminal status: nothing", "CANC", docNext, map[string]Gate{"PAPV": skip}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextStatusCodes(tt.from, tt.next, tt.ungated)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NextStatusCodes(%s) = %v, want %v", tt.from, got, tt.want)
			}
		})
	}
}

func TestPassThroughGate(t *testing.T) {
	skip := Gate{StatusCode: "PAPV", TargetStatusCode: "APPV", SkipTargetWhenUngated: true}
	keep := Gate{StatusCode: "PAPV", TargetStatusCode: "APPV"}

	tests := []struct {
		name    string
		from    string
		to      string
		next    func(string) []string
		ungated map[string]Gate
		wantOK  bool
	}{
		{"waypoint: Draft -> Sent passes through", "DRFT", "SENT", docNext, map[string]Gate{"PAPV": skip}, true},
		{"waypoint: Draft -> Approved is not offered (it is skipped)", "DRFT", "APPV", docNext, map[string]Gate{"PAPV": skip}, false},
		{"waypoint: the gate's own back-to-Draft move is not a move", "DRFT", "DRFT", docNext, map[string]Gate{"PAPV": skip}, false},
		{"resting: Draft -> Approved passes through", "DRFT", "APPV", restingNext, map[string]Gate{"PAPV": keep}, true},
		{"resting: Draft -> Cancel is static, not a pass-through", "DRFT", "CANC", restingNext, map[string]Gate{"PAPV": keep}, false},
		{"gate configured: nothing passes through", "DRFT", "SENT", docNext, nil, false},
		{"not adjacent to the gate: nothing passes through", "SENT", "DRFT", docNext, map[string]Gate{"PAPV": skip}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate, ok := PassThroughGate(tt.from, tt.to, tt.next, tt.ungated)
			if ok != tt.wantOK {
				t.Fatalf("PassThroughGate(%s->%s) ok = %v, want %v", tt.from, tt.to, ok, tt.wantOK)
			}
			if ok && gate.StatusCode != "PAPV" {
				t.Fatalf("PassThroughGate(%s->%s) gate = %+v, want the PAPV gate", tt.from, tt.to, gate)
			}
		})
	}
}
