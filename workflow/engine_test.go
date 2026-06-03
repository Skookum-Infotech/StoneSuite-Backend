package workflow

import (
	"errors"
	"testing"
)

// testDef builds a small Lead-like workflow: new -> contacted -> qualified,
// with a guard requiring "phone" on contacted -> qualified.
func testDef() *Definition {
	return &Definition{
		Workflow: Workflow{ID: "wf", Key: "lead", Name: "Lead", Enabled: true},
		States: []State{
			{ID: "s_new", WorkflowID: "wf", Key: "new", Name: "New", IsInitial: true},
			{ID: "s_contacted", WorkflowID: "wf", Key: "contacted", Name: "Contacted"},
			{ID: "s_qualified", WorkflowID: "wf", Key: "qualified", Name: "Qualified", IsTerminal: true},
		},
		Transitions: []Transition{
			{ID: "t1", FromStateID: "s_new", ToStateID: "s_contacted", Name: "Contact"},
			{ID: "t2", FromStateID: "s_contacted", ToStateID: "s_qualified", Name: "Qualify",
				Guard: Guard{RequiredFields: []string{"phone"}}},
		},
	}
}

func TestValidateTransition(t *testing.T) {
	e := NewEngine()
	def := testDef()

	tests := []struct {
		name       string
		from       string
		to         string
		custom     map[string]any
		wantErr    bool
		wantTransI string
	}{
		{"valid simple", "s_new", "s_contacted", nil, false, "t1"},
		{"no edge", "s_new", "s_qualified", nil, true, ""},
		{"unknown target", "s_new", "s_ghost", nil, true, ""},
		{"same state", "s_new", "s_new", nil, true, ""},
		{"guard blocks without phone", "s_contacted", "s_qualified", nil, true, ""},
		{"guard passes with phone", "s_contacted", "s_qualified", map[string]any{"phone": "555"}, false, "t2"},
		{"guard blocked by empty phone", "s_contacted", "s_qualified", map[string]any{"phone": "  "}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &Record{ID: "r1", WorkflowID: "wf", CurrentStateID: tc.from, CustomFields: tc.custom}
			tr, err := e.ValidateTransition(def, rec, tc.to)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				var te TransitionError
				if !errors.As(err, &te) {
					t.Fatalf("expected TransitionError, got %T", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tr.ID != tc.wantTransI {
				t.Fatalf("transition id = %q, want %q", tr.ID, tc.wantTransI)
			}
		})
	}
}

func TestUnmetRequiredFieldsChecksCoreAndCustom(t *testing.T) {
	g := Guard{RequiredFields: []string{"phone", "name"}}
	rec := &Record{
		CustomFields: map[string]any{"phone": "555"},
		CoreFields:   map[string]any{"name": "Acme"},
	}
	if missing := unmetRequiredFields(g, rec); len(missing) != 0 {
		t.Fatalf("expected none missing, got %v", missing)
	}
	rec2 := &Record{CustomFields: map[string]any{}, CoreFields: map[string]any{}}
	if missing := unmetRequiredFields(g, rec2); len(missing) != 2 {
		t.Fatalf("expected 2 missing, got %v", missing)
	}
}

func TestInitialAndLookupHelpers(t *testing.T) {
	def := testDef()
	init, ok := def.initialState()
	if !ok || init.ID != "s_new" {
		t.Fatalf("initialState = %+v ok=%v", init, ok)
	}
	if _, ok := def.stateByID("s_qualified"); !ok {
		t.Fatal("stateByID should find s_qualified")
	}
	if _, ok := def.transition("s_new", "s_contacted"); !ok {
		t.Fatal("transition new->contacted should exist")
	}
}
