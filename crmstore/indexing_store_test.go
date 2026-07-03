package crmstore

import (
	"context"
	"errors"
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
