//go:build dbtest

package aisettings

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testTenantPool connects to the tenant-schema test database, resetting the
// singleton ai_settings row so tests don't inherit state left by whichever
// test ran before them (Go does not guarantee execution order across test
// functions).
func testTenantPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `DELETE FROM ai_settings`); err != nil {
		t.Fatalf("reset ai_settings: %v", err)
	}
	return pool
}

// testCPPool connects to the control-plane test database, resetting the
// singleton platform_ai_settings row.
func testCPPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test cp db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `DELETE FROM platform_ai_settings`); err != nil {
		t.Fatalf("reset platform_ai_settings: %v", err)
	}
	return pool
}

func TestPlatformEnabled_NoRowYet_DefaultsTrue(t *testing.T) {
	pool := testCPPool(t)
	got, err := PlatformEnabled(context.Background(), pool)
	if err != nil {
		t.Fatalf("PlatformEnabled() error = %v, want nil", err)
	}
	if !got {
		t.Errorf("PlatformEnabled() = false, want true (a missing row means enabled)")
	}
}

func TestSetPlatformEnabled_ThenGet_RoundTrips(t *testing.T) {
	pool := testCPPool(t)
	ctx := context.Background()

	if err := SetPlatformEnabled(ctx, pool, false, "admin@example.com"); err != nil {
		t.Fatalf("SetPlatformEnabled() error = %v", err)
	}
	got, err := PlatformEnabled(ctx, pool)
	if err != nil {
		t.Fatalf("PlatformEnabled() error = %v", err)
	}
	if got {
		t.Errorf("PlatformEnabled() = true, want false after SetPlatformEnabled(false)")
	}

	if err := SetPlatformEnabled(ctx, pool, true, "admin@example.com"); err != nil {
		t.Fatalf("SetPlatformEnabled() error = %v", err)
	}
	got, err = PlatformEnabled(ctx, pool)
	if err != nil {
		t.Fatalf("PlatformEnabled() error = %v", err)
	}
	if !got {
		t.Errorf("PlatformEnabled() = false, want true after SetPlatformEnabled(true)")
	}
}

func TestTenantEnabled_NoRowYet_DefaultsTrue(t *testing.T) {
	pool := testTenantPool(t)
	got, err := TenantEnabled(context.Background(), pool)
	if err != nil {
		t.Fatalf("TenantEnabled() error = %v, want nil", err)
	}
	if !got {
		t.Errorf("TenantEnabled() = false, want true (a missing row means enabled)")
	}
}

func TestSetTenantEnabled_ThenGet_RoundTrips(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	if err := SetTenantEnabled(ctx, pool, false, "user@example.com"); err != nil {
		t.Fatalf("SetTenantEnabled() error = %v", err)
	}
	got, err := TenantEnabled(ctx, pool)
	if err != nil {
		t.Fatalf("TenantEnabled() error = %v", err)
	}
	if got {
		t.Errorf("TenantEnabled() = true, want false after SetTenantEnabled(false)")
	}
}

func TestCache_Status_ReadsThroughAndCaches(t *testing.T) {
	cpPool := testCPPool(t)
	tenantPool := testTenantPool(t)
	ctx := context.Background()

	if err := SetTenantEnabled(ctx, tenantPool, false, "user@example.com"); err != nil {
		t.Fatalf("SetTenantEnabled() error = %v", err)
	}

	c := NewCache(nil)
	status, err := c.Status(ctx, cpPool, tenantPool, "tenant-x")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.PlatformEnabled || status.TenantEnabled || status.Available {
		t.Fatalf("Status() = %+v, want platform on, tenant off, unavailable", status)
	}

	// Flip the underlying row without invalidating: the cached value must
	// still be served until InvalidateTenant is called.
	if err := SetTenantEnabled(ctx, tenantPool, true, "user@example.com"); err != nil {
		t.Fatalf("SetTenantEnabled() error = %v", err)
	}
	status, err = c.Status(ctx, cpPool, tenantPool, "tenant-x")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.TenantEnabled {
		t.Fatalf("Status() should still be serving the cached (stale) value before invalidation")
	}

	c.InvalidateTenant("tenant-x")
	status, err = c.Status(ctx, cpPool, tenantPool, "tenant-x")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.TenantEnabled || !status.Available {
		t.Fatalf("Status() after invalidation = %+v, want tenant on, available", status)
	}
}
