package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"stonesuite-backend/tenancy"
)

func TestSelectStaleTenants(t *testing.T) {
	const cur = "h2"
	mk := func(id, status, db, hash, ms string) tenancy.Tenant {
		return tenancy.Tenant{ID: id, Status: status, DBName: db, SchemaHash: hash, MigrationStatus: ms}
	}
	tests := []struct {
		name string
		in   []tenancy.Tenant
		want []string
	}{
		{"matching hash and ok skipped", []tenancy.Tenant{mk("a", tenancy.StatusActive, "db", cur, tenancy.MigrationOK)}, nil},
		{"stale hash", []tenancy.Tenant{mk("a", tenancy.StatusActive, "db", "h1", tenancy.MigrationOK)}, []string{"a"}},
		{"empty hash", []tenancy.Tenant{mk("a", tenancy.StatusActive, "db", "", tenancy.MigrationOK)}, []string{"a"}},
		{"stuck status", []tenancy.Tenant{mk("a", tenancy.StatusActive, "db", cur, tenancy.MigrationFailed)}, []string{"a"}},
		{"inactive skipped", []tenancy.Tenant{mk("a", tenancy.StatusSuspended, "db", "h1", tenancy.MigrationOK)}, nil},
		{"no db skipped", []tenancy.Tenant{mk("a", tenancy.StatusActive, "", "h1", tenancy.MigrationOK)}, nil},
		{"mixed", []tenancy.Tenant{
			mk("a", tenancy.StatusActive, "db", cur, tenancy.MigrationOK),
			mk("b", tenancy.StatusActive, "db", "old", tenancy.MigrationOK),
		}, []string{"b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, s := range selectStaleTenants(tc.in, cur) {
				got = append(got, s.ID)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func staleN(n int) []tenancy.Tenant {
	out := make([]tenancy.Tenant, n)
	for i := range out {
		out[i] = tenancy.Tenant{ID: string(rune('a' + i)), Slug: string(rune('a' + i))}
	}
	return out
}

func TestRunMigrationsConcurrencyBoundAndCallbacks(t *testing.T) {
	stale := staleN(10)
	gate := tenancy.NewMigrationGate()
	for _, s := range stale {
		gate.Begin(s.ID)
	}
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	migrated := map[string]bool{}
	migrate := func(_ context.Context, tn tenancy.Tenant) error {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		if tn.ID == "c" {
			return errors.New("boom")
		}
		return nil
	}
	runMigrations(context.Background(), gate, stale, bootMigrateConcurrency, migrate, func(id string) {
		mu.Lock()
		migrated[id] = true
		mu.Unlock()
	})
	assert.LessOrEqual(t, int(peak.Load()), bootMigrateConcurrency)
	assert.Len(t, migrated, 9)
	assert.False(t, migrated["c"], "failed tenant must not signal onMigrated")
	for _, s := range stale {
		assert.False(t, gate.Migrating(s.ID), "gate released for %s", s.ID)
	}
}

func TestRunMigrationsContextCancel(t *testing.T) {
	stale := staleN(8)
	gate := tenancy.NewMigrationGate()
	for _, s := range stale {
		gate.Begin(s.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int32
	migrate := func(ctx context.Context, _ tenancy.Tenant) error {
		started.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		runMigrations(ctx, gate, stale, 2, migrate, nil)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runMigrations did not return after cancel")
	}
	assert.LessOrEqual(t, int(started.Load()), 4)
	for _, s := range stale {
		assert.False(t, gate.Migrating(s.ID))
	}
}
