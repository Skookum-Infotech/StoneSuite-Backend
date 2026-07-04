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
	const wfUUID = "11111111-1111-1111-1111-111111111111"
	store := &fakeLoaderStore{
		rec: &workflow.Record{
			ID: "rec-1", WorkflowID: wfUUID, CurrentStateID: "st-2",
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
	if wfID != wfUUID || owner != "user-1" || team != "team-1" {
		t.Fatalf("scope columns = %q,%q,%q; want %s,user-1,team-1", wfID, owner, team, wfUUID)
	}
}

// TestRAGRecordLoaderLoadRejectsNonUUIDWorkflowID covers the v2 relational
// CRM store, which reuses Record.WorkflowID for a fixed type-key string
// (lead/prospect/customer — see crmstore/relational_store.go) rather than a
// real workflows.id UUID like the v1 JSONB store. rag_chunks.workflow_id is a
// UUID column, so a type-key string must never reach it — Load must fall
// back to empty (mapped to SQL NULL by RagStore.Upsert) instead of forwarding
// a value that would blow up the insert with SQLSTATE 22P02.
func TestRAGRecordLoaderLoadRejectsNonUUIDWorkflowID(t *testing.T) {
	store := &fakeLoaderStore{
		rec: &workflow.Record{
			ID: "rec-1", WorkflowID: "lead", CurrentStateID: "st-1",
		},
		key:      "lead",
		statuses: []workflow.StatusInfo{{StateID: "st-1", StatusLabel: "New"}},
	}
	l := NewRAGRecordLoader(store, nil)

	_, wfID, _, _, err := l.Load(ctx(t), "rec-1")
	if err != nil {
		t.Fatal(err)
	}
	if wfID != "" {
		t.Fatalf("wfID = %q, want empty for a non-UUID WorkflowID", wfID)
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
