package crmstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

func ctx(t *testing.T) context.Context { t.Helper(); return context.Background() }

var errBoom = errors.New("boom")

// fakeStore embeds the (nil) Store interface and overrides only the methods
// each test exercises; calling any other method would panic on the nil
// embedded interface, which is fine — no test here calls one.
type fakeStore struct {
	Store
	createID  string
	createErr error
	deleteErr error
	decideErr error
}

func (f *fakeStore) Approve(_ context.Context, _ *pgxpool.Pool, id, _ string, _ bool) (*workflow.Record, error) {
	return &workflow.Record{ID: id}, f.decideErr
}

func (f *fakeStore) Reject(_ context.Context, _ *pgxpool.Pool, id, _, _ string, _ bool) (*workflow.Record, error) {
	return &workflow.Record{ID: id}, f.decideErr
}

func (f *fakeStore) CreateRecord(_ context.Context, _ *pgxpool.Pool, _ string, _ CreateInput) (*workflow.Record, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &workflow.Record{ID: f.createID}, nil
}

func (f *fakeStore) DeleteRecord(_ context.Context, _ *pgxpool.Pool, _ string) error {
	return f.deleteErr
}

type fakeEnqueuer struct {
	calls []string
	err   error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, sourceID, op string) error {
	f.calls = append(f.calls, op+":"+sourceID)
	return f.err
}

func TestIndexingStoreEnqueuesOnCreate(t *testing.T) {
	inner := &fakeStore{createID: "rec-9"}
	enq := &fakeEnqueuer{}
	st := NewIndexingStore(inner, enq)

	_, err := st.CreateRecord(ctx(t), nil, "lead", CreateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got := enq.calls; len(got) != 1 || got[0] != "upsert:rec-9" {
		t.Fatalf("expected upsert enqueue for rec-9, got %v", got)
	}
}

func TestIndexingStoreEnqueuesDeleteWithoutBlocking(t *testing.T) {
	inner := &fakeStore{}
	enq := &fakeEnqueuer{err: errBoom} // enqueue failure must NOT fail the write
	st := NewIndexingStore(inner, enq)
	if err := st.DeleteRecord(ctx(t), nil, "rec-3"); err != nil {
		t.Fatalf("write must succeed even if enqueue errs: %v", err)
	}
	if got := enq.calls; len(got) != 1 || got[0] != "delete:rec-3" {
		t.Fatalf("expected delete enqueue for rec-3, got %v", got)
	}
}

func TestIndexingStoreDoesNotEnqueueOnCreateError(t *testing.T) {
	inner := &fakeStore{createErr: errBoom}
	enq := &fakeEnqueuer{}
	st := NewIndexingStore(inner, enq)

	if _, err := st.CreateRecord(ctx(t), nil, "lead", CreateInput{}); err == nil {
		t.Fatal("expected create error to propagate")
	}
	if len(enq.calls) != 0 {
		t.Fatalf("must not enqueue on a failed write, got %v", enq.calls)
	}
}

// TestIndexingStoreReindexesOnApprovalDecisions: approval status is part of a
// record's indexed text, so approve/reject must refresh its vector like any
// other write — and a failed decision must not.
func TestIndexingStoreReindexesOnApprovalDecisions(t *testing.T) {
	enq := &fakeEnqueuer{}
	st := NewIndexingStore(&fakeStore{}, enq)
	if _, err := st.Approve(ctx(t), nil, "rec-1", "id-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reject(ctx(t), nil, "rec-2", "id-1", "no", false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(enq.calls, ","); got != "upsert:rec-1,upsert:rec-2" {
		t.Fatalf("calls = %s", got)
	}

	failing := &fakeEnqueuer{}
	st = NewIndexingStore(&fakeStore{decideErr: errBoom}, failing)
	_, _ = st.Approve(ctx(t), nil, "rec-3", "id-1", false)
	_, _ = st.Reject(ctx(t), nil, "rec-3", "id-1", "no", false)
	if len(failing.calls) != 0 {
		t.Fatalf("a failed decision must not enqueue: %v", failing.calls)
	}
}
