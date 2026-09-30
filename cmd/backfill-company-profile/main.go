// Command backfill-company-profile seeds each active tenant's Company Info
// profile (see companyprofile/) and first Location (see companylocation/)
// from whatever it already gave at onboarding.
//
// Onboarding (OnboardingForm.tsx) collects this into the control-plane
// tenants.metadata JSON blob for the platform-admin approval review. New
// tenants have it copied into their own database automatically when they are
// provisioned (see onboardingseed/); this is the one-time catch-up for tenants
// that were provisioned before that step existed. It does not run on every
// boot and nothing calls it automatically.
//
// Each half is left untouched (not overwritten) when:
//   - the metadata has no usable company_name / location_name (nothing to
//     write), or
//   - the company_profile row is already non-empty, or the tenant has ever had
//     a location (never clobber real data, including anything saved by hand
//     from the Company Info page, and never resurrect a deleted location).
//
// Usage:
//
//	go run ./cmd/backfill-company-profile \
//	  [--tenant slug] [--tenant slug] …   # default: every active tenant
//	  [--dry-run]                         # report what would be written, change nothing
//
// Reads CONTROL_PLANE_DB_URL and (if set) SECRET_ENCRYPTION_KEY from the
// environment / .env file, same as the server. Idempotent: a second run
// writes nothing new, since every tenant it touched now has a non-empty row.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"stonesuite-backend/config"
	"stonesuite-backend/onboardingseed"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

type tenantFlags []string

func (t *tenantFlags) String() string { return fmt.Sprint([]string(*t)) }
func (t *tenantFlags) Set(v string) error {
	*t = append(*t, v)
	return nil
}

func main() {
	var (
		tenants tenantFlags
		dryRun  bool
	)
	flag.Var(&tenants, "tenant", "tenant slug to backfill; repeatable; default is every active tenant")
	flag.BoolVar(&dryRun, "dry-run", false, "report what would be written, then change nothing")
	flag.Parse()

	config.Load()
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	ctx := context.Background()

	cp, err := tenancy.NewControlPlane(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}

	// Same DSN-decryption seam as main.go: encrypted at rest in prod,
	// plaintext in dev.
	var dsnResolver tenancy.DSNResolver
	if config.AppConfig.SecretEncryptionKey != "" {
		cipher, cErr := secret.New(config.AppConfig.SecretEncryptionKey)
		if cErr != nil {
			log.Fatalf("invalid SECRET_ENCRYPTION_KEY: %v", cErr)
		}
		dsnResolver = func(_ context.Context, t *tenancy.Tenant) (string, error) {
			return cipher.Decrypt(t.DBConnectionRef)
		}
	}
	router := tenancy.NewRouter(dsnResolver)

	targets, err := resolveTenants(ctx, cp, tenants)
	if err != nil {
		log.Fatalf("resolve tenants: %v", err)
	}
	if len(targets) == 0 {
		log.Fatal("no tenants to process")
	}

	var written, skipped int
	for _, t := range targets {
		// Unlike ListTenants (a platform-admin browse view), this tool only
		// touches tenants with a real, migrated database -- anything else
		// (invited/submitted/provisioning/rejected/deleted) has no
		// company_profile table to write into, and one bad connection
		// shouldn't abort every other tenant's backfill.
		if t.Status != "active" {
			log.Printf("tenant %s: skipped (status=%s, not active)", t.Slug, t.Status)
			skipped++
			continue
		}
		res, err := backfillTenant(ctx, router, t, dryRun)
		if err != nil {
			log.Printf("tenant %s: ERROR: %v", t.Slug, err)
			skipped++
			continue
		}
		if !res.Any() {
			log.Printf("tenant %s: skipped (nothing to seed: no usable onboarding data, or already set)", t.Slug)
			skipped++
			continue
		}
		log.Printf("tenant %s: %s %s", t.Slug, describe(res), verb(dryRun))
		written++
	}
	log.Printf("done: %d %s, %d skipped, %d tenant(s) total", written, verb(dryRun), skipped, len(targets))
}

func verb(dryRun bool) string {
	if dryRun {
		return "would be written"
	}
	return "written"
}

func resolveTenants(ctx context.Context, cp *tenancy.ControlPlane, slugs tenantFlags) ([]tenancy.Tenant, error) {
	if len(slugs) == 0 {
		return cp.ListTenants(ctx)
	}
	out := make([]tenancy.Tenant, 0, len(slugs))
	for _, slug := range slugs {
		t, err := cp.TenantBySlug(ctx, slug)
		if err != nil {
			return nil, fmt.Errorf("tenant %q: %w", slug, err)
		}
		out = append(out, *t)
	}
	return out, nil
}

// describe names what a Result covers, for the per-tenant log line.
func describe(res onboardingseed.Result) string {
	switch {
	case res.Profile && res.Location:
		return "company profile + location"
	case res.Profile:
		return "company profile"
	default:
		return "location"
	}
}

// backfillTenant writes t's onboarding-metadata company profile and location
// into its own database. The Result says what was actually (or, on dry-run,
// would be) written; an empty Result with a nil error means there was nothing
// to do.
func backfillTenant(ctx context.Context, router *tenancy.Router, t tenancy.Tenant, dryRun bool) (onboardingseed.Result, error) {
	pool, err := router.PoolFor(ctx, &t)
	if err != nil {
		return onboardingseed.Result{}, fmt.Errorf("tenant pool: %w", err)
	}
	res, err := onboardingseed.Seed(ctx, pool, t.Metadata, dryRun)
	if err != nil {
		return res, fmt.Errorf("seed company info: %w", err)
	}
	return res, nil
}
