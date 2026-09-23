package controllers

import (
	"context"
	"testing"
	"time"
)

func TestAILimiter_GlobalCap(t *testing.T) {
	l := newAILimiter(2, 2)
	ctx := context.Background()
	r1, ok1 := l.acquire(ctx, "a")
	_, ok2 := l.acquire(ctx, "b")
	if !ok1 || !ok2 {
		t.Fatal("first two acquires must succeed")
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, ok := l.acquire(short, "c"); ok {
		t.Fatal("a third concurrent slot must not be granted")
	}
	r1()
	if _, ok := l.acquire(ctx, "c"); !ok {
		t.Fatal("a released slot must be reusable")
	}
}

// TestAILimiter_PerTenantCap: one tenant can't take every slot even when
// global capacity is free.
func TestAILimiter_PerTenantCap(t *testing.T) {
	l := newAILimiter(2, 1)
	ctx := context.Background()
	if _, ok := l.acquire(ctx, "a"); !ok {
		t.Fatal("first acquire must succeed")
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, ok := l.acquire(short, "a"); ok {
		t.Fatal("a tenant must not exceed its per-tenant cap")
	}
	if _, ok := l.acquire(ctx, "b"); !ok {
		t.Fatal("another tenant must still get the free global slot")
	}
}

// TestAILimiter_WaiterWakesOnRelease: a queued request gets the slot as soon
// as one frees, rather than failing immediately or polling.
func TestAILimiter_WaiterWakesOnRelease(t *testing.T) {
	l := newAILimiter(1, 1)
	release, _ := l.acquire(context.Background(), "a")
	got := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, ok := l.acquire(ctx, "b")
		got <- ok
	}()
	time.Sleep(20 * time.Millisecond)
	release()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("the waiter should have been granted the released slot")
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter was never woken")
	}
}

func TestAILimiter_ReleaseIsIdempotent(t *testing.T) {
	l := newAILimiter(1, 1)
	release, _ := l.acquire(context.Background(), "a")
	release()
	release()
	if l.inUse != 0 || len(l.byTenant) != 0 {
		t.Fatalf("double release corrupted counts: inUse=%d byTenant=%v", l.inUse, l.byTenant)
	}
}
