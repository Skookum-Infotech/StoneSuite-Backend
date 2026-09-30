// Package onboardingseed copies the company details a customer gave at
// onboarding into the tenant's own database: the Company Info profile
// (Configuration -> Company Info) and the applicant's primary location
// (Configuration -> Company Info -> Locations).
//
// Onboarding (OnboardingForm.tsx) stores everything in the control-plane
// tenants.metadata JSON blob for the platform-admin approval review. Nothing
// in the tenant's own database knows about it until this package copies it
// over -- which the provisioner does once the tenant's migrations have run,
// and cmd/backfill-company-profile does for tenants provisioned before that
// step existed.
package onboardingseed

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/companylocation"
	"stonesuite-backend/companyprofile"
)

// Result reports what Seed wrote -- or, on a dry run, would have written.
type Result struct {
	Profile  bool
	Location bool
}

// Any reports whether Seed wrote (or would write) anything at all.
func (r Result) Any() bool { return r.Profile || r.Location }

// Seed copies the onboarding metadata into pool, one-time and idempotently:
//
//   - the company profile is written only while the tenant's company_profile
//     is still entirely empty, so anything saved by hand from the Company
//     Info page is never overwritten;
//   - the location is written only while the tenant has never had one (deleted
//     ones count), and becomes the tenant's default location as its first.
//
// Metadata with no usable company name / location name simply skips that half.
// The two halves are independent: a failure in one does not stop the other,
// and every failure is reported (joined) so a caller can log it. With dryRun
// set nothing is written and the Result says what would have been.
func Seed(ctx context.Context, pool *pgxpool.Pool, metadataJSON string, dryRun bool) (Result, error) {
	var (
		res  Result
		errs []error
	)

	var err error
	if res.Profile, err = seedProfile(ctx, pool, metadataJSON, dryRun); err != nil {
		errs = append(errs, err)
	}
	if res.Location, err = seedLocation(ctx, pool, metadataJSON, dryRun); err != nil {
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}

func seedProfile(ctx context.Context, pool *pgxpool.Pool, metadataJSON string, dryRun bool) (bool, error) {
	profile, ok := companyprofile.FromOnboardingMetadata(metadataJSON)
	if !ok {
		return false, nil
	}
	existing, err := companyprofile.Get(ctx, pool)
	if err != nil {
		return false, fmt.Errorf("check existing company profile: %w", err)
	}
	if *existing != (companyprofile.Profile{}) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := companyprofile.Upsert(ctx, pool, profile); err != nil {
		return false, fmt.Errorf("seed company profile: %w", err)
	}
	return true, nil
}

func seedLocation(ctx context.Context, pool *pgxpool.Pool, metadataJSON string, dryRun bool) (bool, error) {
	in, ok := companylocation.FromOnboardingMetadata(metadataJSON)
	if !ok {
		return false, nil
	}
	has, err := companylocation.HasAny(ctx, pool)
	if err != nil {
		return false, err
	}
	if has {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	// No acting employee: this runs at provisioning, before the tenant's first
	// user has done anything, so created_by is left NULL.
	if _, err := companylocation.Create(ctx, pool, in, 0); err != nil {
		return false, fmt.Errorf("seed company location: %w", err)
	}
	return true, nil
}
