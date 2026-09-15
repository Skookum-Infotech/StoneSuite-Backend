// Command backfill-company-profile seeds each active tenant's new
// company_profile row (see companyprofile/) from whatever company name/
// address it already gave at onboarding.
//
// Onboarding (OnboardingForm.tsx) collects this into the control-plane
// tenants.metadata JSON blob for the platform-admin approval review — it was
// never written to the tenant's own database, since company_profile did not
// exist until now. This is a one-time catch-up for tenants that already went
// through onboarding before that table existed; it does not run on every
// boot and nothing calls it automatically.
//
// A tenant is left untouched (not overwritten) when:
//   - its metadata has no usable company_name (nothing to write), or
//   - its company_profile row is already non-empty (never clobber real data,
//     including anything already saved by hand via the Company Info page).
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

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/config"
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
		did, reason, err := backfillTenant(ctx, router, t, dryRun)
		if err != nil {
			log.Printf("tenant %s: ERROR: %v", t.Slug, err)
			skipped++
			continue
		}
		if !did {
			log.Printf("tenant %s: skipped (%s)", t.Slug, reason)
			skipped++
			continue
		}
		log.Printf("tenant %s: company profile %s", t.Slug, verb(dryRun))
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

// backfillTenant writes t's onboarding-metadata company profile into its own
// database. did is true only when a row was actually (or, on dry-run, would
// be) written; reason explains a false without error.
func backfillTenant(ctx context.Context, router *tenancy.Router, t tenancy.Tenant, dryRun bool) (did bool, reason string, err error) {
	profile, ok := parseMetadataProfile(t.Metadata)
	if !ok {
		return false, "no company_name in onboarding metadata", nil
	}

	pool, err := router.PoolFor(ctx, &t)
	if err != nil {
		return false, "", fmt.Errorf("tenant pool: %w", err)
	}

	existing, err := companyprofile.Get(ctx, pool)
	if err != nil {
		return false, "", fmt.Errorf("check existing company profile: %w", err)
	}
	if *existing != (companyprofile.Profile{}) {
		return false, "company profile already set", nil
	}

	if dryRun {
		return true, "", nil
	}
	if err := companyprofile.Upsert(ctx, pool, profile); err != nil {
		return false, "", fmt.Errorf("upsert company profile: %w", err)
	}
	return true, "", nil
}
