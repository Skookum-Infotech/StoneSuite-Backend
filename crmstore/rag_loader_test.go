package crmstore

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

type fakeLoaderStore struct {
	Store
	rec       *workflow.Record
	key       string
	statuses  []workflow.StatusInfo
	getErr    error
	keyErr    error
	statusErr error
}

func (f *fakeLoaderStore) GetRecord(_ context.Context, _ *pgxpool.Pool, _ string) (*workflow.Record, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.rec, nil
}

func (f *fakeLoaderStore) KeyForRecord(_ context.Context, _ *pgxpool.Pool, _ string) (string, error) {
	if f.keyErr != nil {
		return "", f.keyErr
	}
	return f.key, nil
}

func (f *fakeLoaderStore) AllStatuses(_ context.Context, _ *pgxpool.Pool) ([]workflow.StatusInfo, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return f.statuses, nil
}

func TestRAGRecordLoaderLoadResolvesDocAndScope(t *testing.T) {
	store := &fakeLoaderStore{
		rec: &workflow.Record{
			ID: "rec-1", WorkflowID: "wf-1", CurrentStateID: "st-2",
			OwnerUserID: "user-1", TeamID: "team-1",
			CoreFields:   map[string]any{"company_name": "Acme"},
			CustomFields: map[string]any{"deal_size": 5000},
		},
		key: "prospect",
		statuses: []workflow.StatusInfo{
			{StateID: "st-1", StatusLabel: "New"},
			{StateID: "st-2", StatusLabel: "In Negotiation"},
		},
	}
	l := NewRAGRecordLoader(store, nil)

	doc, wfID, owner, team, err := l.Load(ctx(t), "rec-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.WorkflowKey != "prospect" || doc.StateName != "In Negotiation" {
		t.Fatalf("unexpected doc: %+v", doc)
	}
	if doc.Core["company_name"] != "Acme" || doc.Custom["deal_size"] != 5000 {
		t.Fatalf("fields not carried through: %+v", doc)
	}
	if wfID != "wf-1" || owner != "user-1" || team != "team-1" {
		t.Fatalf("scope columns = %q,%q,%q; want wf-1,user-1,team-1", wfID, owner, team)
	}
}

func TestRAGRecordLoaderPropagatesGetRecordError(t *testing.T) {
	store := &fakeLoaderStore{getErr: errBoom}
	l := NewRAGRecordLoader(store, nil)
	if _, _, _, _, err := l.Load(ctx(t), "rec-1"); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRAGRecordLoaderStateNameFallsBackToEmptyOnLookupFailure(t *testing.T) {
	store := &fakeLoaderStore{
		rec:       &workflow.Record{ID: "rec-1", CurrentStateID: "st-2"},
		key:       "lead",
		statusErr: errBoom,
	}
	l := NewRAGRecordLoader(store, nil)
	doc, _, _, _, err := l.Load(ctx(t), "rec-1")
	if err != nil {
		t.Fatalf("a status-lookup failure must not fail the whole load: %v", err)
	}
	if doc.StateName != "" {
		t.Fatalf("StateName = %q, want empty on lookup failure", doc.StateName)
	}
}
