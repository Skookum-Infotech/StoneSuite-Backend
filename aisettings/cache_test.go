package aisettings

import (
	"testing"
	"time"
)

// fakeClock lets a test move time forward deterministically.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

func TestCache_GetSet(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	c := NewCache(fc.now)

	if _, ok := c.get("platform"); ok {
		t.Fatalf("get on empty cache must miss")
	}

	c.set("platform", true)
	got, ok := c.get("platform")
	if !ok || !got {
		t.Fatalf("get after set = (%v, %v), want (true, true)", got, ok)
	}
}

func TestCache_TTLExpires(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	c := NewCache(fc.now)
	c.set("tenant-1", false)

	fc.t = fc.t.Add(cacheTTL - time.Second)
	if _, ok := c.get("tenant-1"); !ok {
		t.Fatalf("entry must still be fresh just under the TTL")
	}

	fc.t = fc.t.Add(2 * time.Second) // now past cacheTTL since set
	if _, ok := c.get("tenant-1"); ok {
		t.Fatalf("entry must expire once the TTL has elapsed")
	}
}

func TestCache_InvalidatePlatform(t *testing.T) {
	c := NewCache(nil)
	c.set(PlatformKey, true)
	c.InvalidatePlatform()
	if _, ok := c.get(PlatformKey); ok {
		t.Fatalf("InvalidatePlatform must drop the cached entry")
	}
}

func TestCache_InvalidateTenant(t *testing.T) {
	c := NewCache(nil)
	c.set("tenant-a", true)
	c.set("tenant-b", false)

	c.InvalidateTenant("tenant-a")

	if _, ok := c.get("tenant-a"); ok {
		t.Fatalf("InvalidateTenant must drop only the named tenant's entry")
	}
	if v, ok := c.get("tenant-b"); !ok || v != false {
		t.Fatalf("InvalidateTenant must not disturb other tenants' entries")
	}
}
