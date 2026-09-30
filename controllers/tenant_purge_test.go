package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

func TestLifecycleGuard(t *testing.T) {
	owner := &tenancy.Tenant{ID: "t-owner", Slug: "skookum", Status: tenancy.StatusActive, IsPlatformOwner: true}
	acme := &tenancy.Tenant{ID: "t-acme", Slug: "acme", Status: tenancy.StatusActive, DBName: "tenant_acme", R2Bucket: "ss-acme"}
	rejected := &tenancy.Tenant{ID: "t-rej", Slug: "rej", Status: tenancy.StatusRejected}
	provisioning := &tenancy.Tenant{ID: "t-prov", Slug: "newco", Status: tenancy.StatusProvisioning}

	tests := []struct {
		name        string
		tenant      *tenancy.Tenant
		action      string
		confirmSlug string
		wantStatus  int
	}{
		{"suspend a customer", acme, "suspend", "", 0},
		{"restore a customer", acme, "restore", "", 0},
		{"soft delete a customer", acme, "delete", "", 0},
		{"purge with the exact slug", acme, "purge", "acme", 0},
		{"purge a rejected application with the exact slug", rejected, "purge", "rej", 0},

		{"suspend the platform owner", owner, "suspend", "", http.StatusForbidden},
		{"soft delete the platform owner", owner, "delete", "", http.StatusForbidden},
		{"force delete the platform owner", owner, "force-delete", "", http.StatusForbidden},
		{"purge the platform owner even with a matching slug", owner, "purge", "skookum", http.StatusForbidden},
		{"restore the platform owner is harmless", owner, "restore", "", 0},

		{"purge without a confirmation", acme, "purge", "", http.StatusBadRequest},
		{"purge with the wrong slug", acme, "purge", "globex", http.StatusBadRequest},
		{"purge is case sensitive", acme, "purge", "Acme", http.StatusBadRequest},
		{"purge rejects padded whitespace", acme, "purge", " acme", http.StatusBadRequest},

		{"purge while provisioning is running", provisioning, "purge", "newco", http.StatusConflict},
		{"suspend while provisioning is not this guard's concern", provisioning, "suspend", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := lifecycleGuard(tc.tenant, tc.action, tc.confirmSlug)

			assert.Equal(t, tc.wantStatus, status)
			if tc.wantStatus == 0 {
				assert.Empty(t, msg)
			} else {
				assert.NotEmpty(t, msg, "a refusal carries a user-facing message")
			}
		})
	}
}

// purgeRecorder builds purgeOps whose every call is appended to calls, with
// optional per-step failures.
type purgeRecorder struct {
	calls []string
	fail  map[string]error
}

func (r *purgeRecorder) ops() purgeOps {
	rec := func(name string) error {
		r.calls = append(r.calls, name)
		return r.fail[name]
	}
	return purgeOps{
		markDeleted:   func(_ context.Context, id string) error { return rec("markDeleted " + id) },
		evictPool:     func(id string) { r.calls = append(r.calls, "evict "+id) },
		emptyBucket:   func(_ context.Context, b string) error { return rec("emptyBucket " + b) },
		deleteBucket:  func(_ context.Context, b string) error { return rec("deleteBucket " + b) },
		dropDatabase:  func(_ context.Context, d string) error { return rec("dropDatabase " + d) },
		deleteRecords: func(_ context.Context, id string) error { return rec("deleteRecords " + id) },
	}
}

var provisionedTenant = &tenancy.Tenant{ID: "t-acme", Slug: "acme", DBName: "tenant_acme", R2Bucket: "ss-acme"}

func TestRunPurge_DestroysInOrder(t *testing.T) {
	rec := &purgeRecorder{}

	failed, err := runPurge(context.Background(), provisionedTenant, rec.ops())

	require.NoError(t, err)
	assert.Empty(t, failed)
	assert.Equal(t, []string{
		"markDeleted t-acme",
		"evict t-acme",
		"emptyBucket ss-acme",
		"deleteBucket ss-acme",
		"dropDatabase tenant_acme",
		"deleteRecords t-acme",
	}, rec.calls, "unservable first, external resources next, the row that lets you retry last")
}

func TestRunPurge_SkipsResourcesTheTenantNeverGot(t *testing.T) {
	// A rejected or never-provisioned application has no database and no bucket.
	rec := &purgeRecorder{}
	bare := &tenancy.Tenant{ID: "t-rej", Slug: "rej"}

	_, err := runPurge(context.Background(), bare, rec.ops())

	require.NoError(t, err)
	assert.Equal(t, []string{"markDeleted t-rej", "evict t-rej", "deleteRecords t-rej"}, rec.calls)
}

func TestRunPurge_BucketWithoutDatabase(t *testing.T) {
	rec := &purgeRecorder{}
	bucketOnly := &tenancy.Tenant{ID: "t-b", Slug: "b", R2Bucket: "ss-b"}

	_, err := runPurge(context.Background(), bucketOnly, rec.ops())

	require.NoError(t, err)
	assert.Equal(t, []string{
		"markDeleted t-b", "evict t-b", "emptyBucket ss-b", "deleteBucket ss-b", "deleteRecords t-b",
	}, rec.calls)
}

func TestRunPurge_AbortsBeforeDestroyingAnythingWhenTeardownIsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*purgeOps)
	}{
		{"no storage client to empty the bucket", func(o *purgeOps) { o.emptyBucket = nil }},
		{"no cloudflare client to delete the bucket", func(o *purgeOps) { o.deleteBucket = nil }},
		{"no provisioner to drop the database", func(o *purgeOps) { o.dropDatabase = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &purgeRecorder{}
			ops := rec.ops()
			tc.mutate(&ops)

			failed, err := runPurge(context.Background(), provisionedTenant, ops)

			require.Error(t, err)
			assert.True(t, errors.Is(err, errPurgeUnavailable), "got %v", err)
			assert.Equal(t, purgeStepPreflight, failed)
			assert.Empty(t, rec.calls, "nothing — not even the status flip — may run")
		})
	}
}

func TestRunPurge_MissingTeardownIsIgnoredWhenTenantNeverNeededIt(t *testing.T) {
	rec := &purgeRecorder{}
	ops := rec.ops()
	ops.emptyBucket, ops.deleteBucket, ops.dropDatabase = nil, nil, nil
	bare := &tenancy.Tenant{ID: "t-rej", Slug: "rej"}

	_, err := runPurge(context.Background(), bare, ops)

	require.NoError(t, err, "a never-provisioned tenant must be purgeable on a server with no R2/Cloudflare")
}

func TestRunPurge_StopsAtFirstFailureAndNamesTheStep(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		failOn    string
		wantStep  string
		wantCalls []string
	}{
		{
			"marking deleted fails", "markDeleted t-acme", purgeStepMarkDeleted,
			[]string{"markDeleted t-acme"},
		},
		{
			"emptying the bucket fails", "emptyBucket ss-acme", purgeStepStorage,
			[]string{"markDeleted t-acme", "evict t-acme", "emptyBucket ss-acme"},
		},
		{
			"deleting the bucket fails", "deleteBucket ss-acme", purgeStepStorage,
			[]string{"markDeleted t-acme", "evict t-acme", "emptyBucket ss-acme", "deleteBucket ss-acme"},
		},
		{
			"dropping the database fails", "dropDatabase tenant_acme", purgeStepDatabase,
			[]string{
				"markDeleted t-acme", "evict t-acme", "emptyBucket ss-acme", "deleteBucket ss-acme",
				"dropDatabase tenant_acme",
			},
		},
		{
			"deleting the records fails", "deleteRecords t-acme", purgeStepRecords,
			[]string{
				"markDeleted t-acme", "evict t-acme", "emptyBucket ss-acme", "deleteBucket ss-acme",
				"dropDatabase tenant_acme", "deleteRecords t-acme",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &purgeRecorder{fail: map[string]error{tc.failOn: boom}}

			failed, err := runPurge(context.Background(), provisionedTenant, rec.ops())

			require.Error(t, err)
			assert.ErrorIs(t, err, boom)
			assert.Equal(t, tc.wantStep, failed)
			assert.Equal(t, tc.wantCalls, rec.calls, "later steps must not run, so a retry can resume")
		})
	}
}

func TestRunPurge_ValidatesTheDatabaseNameBeforeDestroyingAnything(t *testing.T) {
	rec := &purgeRecorder{}
	ops := rec.ops()
	ops.validateDatabase = func(name string) error {
		rec.calls = append(rec.calls, "validate "+name)
		return fmt.Errorf("%q is not a tenant database", name)
	}

	failed, err := runPurge(context.Background(), provisionedTenant, ops)

	require.Error(t, err)
	assert.True(t, errors.Is(err, errPurgeUnsafeDatabase), "got %v", err)
	assert.Equal(t, purgeStepPreflight, failed)
	assert.Equal(t, []string{"validate tenant_acme"}, rec.calls,
		"a bad database name must be caught before the bucket is emptied or the status flipped")
}

func TestRunPurge_ValidatorRunsFirstAndAllowsAGoodName(t *testing.T) {
	rec := &purgeRecorder{}
	ops := rec.ops()
	ops.validateDatabase = func(name string) error {
		rec.calls = append(rec.calls, "validate "+name)
		return nil
	}

	_, err := runPurge(context.Background(), provisionedTenant, ops)

	require.NoError(t, err)
	require.NotEmpty(t, rec.calls)
	assert.Equal(t, "validate tenant_acme", rec.calls[0])
	assert.Contains(t, rec.calls, "dropDatabase tenant_acme")
}

func TestRunPurge_SkipsDatabaseValidationForATenantWithoutOne(t *testing.T) {
	rec := &purgeRecorder{}
	ops := rec.ops()
	ops.validateDatabase = func(string) error { return errors.New("must not be called") }
	bare := &tenancy.Tenant{ID: "t-rej", Slug: "rej"}

	_, err := runPurge(context.Background(), bare, ops)

	require.NoError(t, err)
}
