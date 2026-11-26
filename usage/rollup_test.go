package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

type rollupFakeLister struct {
	tenants []tenancy.Tenant
	err     error
}

func (f rollupFakeLister) ListTenants(context.Context) ([]tenancy.Tenant, error) {
	return f.tenants, f.err
}

type rollupFakePools struct{ failSlug string }

func (f rollupFakePools) PoolFor(_ context.Context, t *tenancy.Tenant) (*pgxpool.Pool, error) {
	if t.Slug == f.failSlug {
		return nil, errors.New("pool down")
	}
	return nil, nil
}

type rollupFakeSource struct {
	deliveriesErr error
	created       map[string]int64
}

func (rollupFakeSource) DBSize(context.Context) (int64, error) { return 5000, nil }
func (rollupFakeSource) TopTables(context.Context) ([]TableSize, error) {
	return []TableSize{{TableName: "audit_logs", TotalBytes: 100, RowEstimate: 7}}, nil
}
func (f rollupFakeSource) RecordsCreated(context.Context, time.Time, time.Time) (map[string]int64, error) {
	return f.created, nil
}
func (rollupFakeSource) DocumentsSent(context.Context, time.Time, time.Time) (int64, error) {
	return 2, nil
}
func (f rollupFakeSource) Deliveries(context.Context, time.Time, time.Time) (int64, int64, error) {
	return 4, 3, f.deliveriesErr
}

type rollupFakeR2 struct {
	calls []string
	err   error
}

func (f *rollupFakeR2) Measure(_ context.Context, tenantID, bucket string) (*StorageSnapshot, error) {
	f.calls = append(f.calls, bucket)
	if f.err != nil {
		return nil, f.err
	}
	return &StorageSnapshot{UsedBytes: 900, ObjectCount: 9}, nil
}

type rollupQuietCall struct {
	tenantID   string
	quietSince time.Time
}

type rollupFakeStore struct {
	mu        sync.Mutex
	hourly    map[string]HourlyTotals
	jobs      map[string]JobStats
	signIn    map[string]SignInStats
	upserts   map[string]map[string]float64
	sizes     map[string][]TableSize
	firings   []AlertFiring
	quiet     []rollupQuietCall
	pruned    int
	failUpsrt string
}

func newRollupFakeStore() *rollupFakeStore {
	return &rollupFakeStore{
		hourly: map[string]HourlyTotals{}, jobs: map[string]JobStats{}, signIn: map[string]SignInStats{},
		upserts: map[string]map[string]float64{}, sizes: map[string][]TableSize{},
	}
}

func (s *rollupFakeStore) HourlyTotals(_ context.Context, id string, _, _ time.Time) (HourlyTotals, error) {
	return s.hourly[id], nil
}
func (s *rollupFakeStore) JobStats(_ context.Context, id string, _, _ time.Time) (JobStats, error) {
	return s.jobs[id], nil
}
func (s *rollupFakeStore) SignInStats(_ context.Context, id string, _, _ time.Time) (SignInStats, error) {
	return s.signIn[id], nil
}
func (s *rollupFakeStore) UpsertDailyMetrics(_ context.Context, id string, _ time.Time, v map[string]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.failUpsrt {
		return errors.New("write failed")
	}
	cp := map[string]float64{}
	for k, x := range v {
		cp[k] = x
	}
	s.upserts[id] = cp
	return nil
}
func (s *rollupFakeStore) ReplaceTableSizes(_ context.Context, id string, _ time.Time, sz []TableSize) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sizes[id] = sz
	return nil
}
func (s *rollupFakeStore) DailyValues(_ context.Context, ids []string, from, to time.Time) ([]DailyValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []DailyValue{
		{TenantID: ids[0], Day: to, Key: MetricActiveUsers, Value: s.upserts[ids[0]][MetricActiveUsers]},
		{TenantID: ids[0], Day: from, Key: MetricRequests, Value: 11},
	}, nil
}
func (s *rollupFakeStore) TokensByDay(context.Context, []string, time.Time, time.Time) ([]DailyValue, error) {
	return nil, nil
}
func (s *rollupFakeStore) RecordFiring(_ context.Context, f AlertFiring, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firings = append(s.firings, f)
	return nil
}
func (s *rollupFakeStore) ResolveQuiet(_ context.Context, id string, quietSince, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quiet = append(s.quiet, rollupQuietCall{id, quietSince})
	return 0, nil
}
func (s *rollupFakeStore) PruneRetention(context.Context, time.Time) error {
	s.pruned++
	return nil
}

func rollupTenant(id, slug, bucket string) tenancy.Tenant {
	return tenancy.Tenant{
		ID: id, Slug: slug, Status: tenancy.StatusActive, MigrationStatus: tenancy.MigrationOK,
		DBName: "tenant_" + slug, R2Bucket: bucket,
	}
}

func newTestRollup(tenants []tenancy.Tenant, pools rollupFakePools, store *rollupFakeStore, r2 r2Measurer, src tenantSource) *Rollup {
	r := NewRollup(rollupFakeLister{tenants: tenants}, pools, store, r2, func() time.Time {
		return time.Date(2026, 11, 26, 2, 41, 0, 0, time.UTC)
	})
	r.newSource = func(*pgxpool.Pool) tenantSource { return src }
	return r
}

var rollupDay = time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

func TestRollup_ComputesMetrics(t *testing.T) {
	store := newRollupFakeStore()
	store.hourly["t1"] = HourlyTotals{Requests: 100, Errors5xx: 3, HandlerMs: 7_200_000, P95Ms: 250}
	store.jobs["t1"] = JobStats{JobMs: 3_600_000, Failed: 2, Dead: 1}
	store.signIn["t1"] = SignInStats{ActiveUsers: 4}
	r2 := &rollupFakeR2{}
	src := rollupFakeSource{created: map[string]int64{"quote": 5}}
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "ss-one")}, rollupFakePools{}, store, r2, src)

	sum, err := r.Run(context.Background(), rollupDay.Add(5*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, RollupSummary{Tenants: 1}, sum)

	want := map[string]float64{
		MetricRequests: 100, MetricErrors5xx: 3, MetricP95Ms: 250,
		MetricComputeRequestHours: 2, MetricComputeJobHours: 1,
		MetricFailedJobs: 2, MetricDeadJobs: 1, MetricActiveUsers: 4,
		MetricDBBytes: 5000, MetricR2Bytes: 900, MetricR2Objects: 9,
		MetricEmailsSent: 5, MetricAutomationRuns: 4, RecordsCreatedKey("quote"): 5,
	}
	assert.Equal(t, want, store.upserts["t1"])
	assert.Equal(t, []string{"ss-one"}, r2.calls)
	assert.Len(t, store.sizes["t1"], 1)
	assert.Equal(t, 1, store.pruned)
}

func TestRollup_NoR2BucketMeansNoR2Keys(t *testing.T) {
	store := newRollupFakeStore()
	r2 := &rollupFakeR2{}
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "")}, rollupFakePools{}, store, r2, rollupFakeSource{})

	_, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	assert.NotContains(t, store.upserts["t1"], MetricR2Bytes)
	assert.NotContains(t, store.upserts["t1"], MetricR2Objects)
	assert.Empty(t, r2.calls)
}

func TestRollup_OptionalSourceFailuresDoNotFailTenant(t *testing.T) {
	store := newRollupFakeStore()
	r2 := &rollupFakeR2{err: errors.New("r2 down")}
	src := rollupFakeSource{deliveriesErr: errors.New("relation does not exist")}
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "ss-one")}, rollupFakePools{}, store, r2, src)

	sum, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	assert.Equal(t, 0, sum.Failed)
	assert.NotContains(t, store.upserts["t1"], MetricR2Bytes)
	assert.NotContains(t, store.upserts["t1"], MetricAutomationRuns)
	assert.Equal(t, float64(2), store.upserts["t1"][MetricEmailsSent])
}

func TestRollup_FailingTenantIsIsolatedAndCounted(t *testing.T) {
	store := newRollupFakeStore()
	tenants := []tenancy.Tenant{rollupTenant("t1", "bad", ""), rollupTenant("t2", "good", "")}
	r := newTestRollup(tenants, rollupFakePools{failSlug: "bad"}, store, nil, rollupFakeSource{})

	sum, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	assert.Equal(t, RollupSummary{Tenants: 2, Failed: 1}, sum)
	assert.NotContains(t, store.upserts, "t1")
	assert.Contains(t, store.upserts, "t2")
	require.Len(t, store.quiet, 1)
	assert.Equal(t, "t2", store.quiet[0].tenantID)
	assert.Equal(t, 1, store.pruned)
}

func TestRollup_WriteFailureCountedOnce(t *testing.T) {
	store := newRollupFakeStore()
	store.failUpsrt = "t1"
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "")}, rollupFakePools{}, store, nil, rollupFakeSource{})

	sum, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Failed)
}

func TestRollup_SkipsNonServableTenants(t *testing.T) {
	store := newRollupFakeStore()
	suspended := rollupTenant("t2", "sus", "")
	suspended.Status = tenancy.StatusSuspended
	unprovisioned := rollupTenant("t3", "new", "")
	unprovisioned.DBName = ""
	tenants := []tenancy.Tenant{rollupTenant("t1", "one", ""), suspended, unprovisioned}
	r := newTestRollup(tenants, rollupFakePools{}, store, nil, rollupFakeSource{})

	sum, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	assert.Equal(t, RollupSummary{Tenants: 1}, sum)
	assert.Len(t, store.upserts, 1)
}

func TestRollup_ListFailureIsReturned(t *testing.T) {
	r := NewRollup(rollupFakeLister{err: errors.New("cp down")}, rollupFakePools{}, newRollupFakeStore(), nil, time.Now)
	_, err := r.Run(context.Background(), rollupDay)
	require.Error(t, err)
}

func TestRollup_RerunIsIdempotent(t *testing.T) {
	store := newRollupFakeStore()
	store.hourly["t1"] = HourlyTotals{Requests: 10, HandlerMs: 1800000}
	store.signIn["t1"] = SignInStats{ActiveUsers: 1}
	src := rollupFakeSource{created: map[string]int64{"invoice": 2}}
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "ss-one")}, rollupFakePools{}, store, &rollupFakeR2{}, src)

	_, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	first := store.upserts["t1"]
	_, err = r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	assert.Equal(t, first, store.upserts["t1"])
}

func TestRollup_ResolveQuietUsesDayMinusTwo(t *testing.T) {
	store := newRollupFakeStore()
	r := newTestRollup([]tenancy.Tenant{rollupTenant("t1", "one", "")}, rollupFakePools{}, store, nil, rollupFakeSource{})

	_, err := r.Run(context.Background(), rollupDay.Add(13*time.Hour))
	require.NoError(t, err)
	require.Len(t, store.quiet, 1)
	assert.Equal(t, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), store.quiet[0].quietSince)
}

func TestRollup_FleetP95FeedsOutlierRule(t *testing.T) {
	store := newRollupFakeStore()
	var tenants []tenancy.Tenant
	ids := []string{"t01", "t02", "t03", "t04", "t05", "t06", "t07", "t08", "t09", "t10",
		"t11", "t12", "t13", "t14", "t15", "t16", "t17", "t18", "t19", "t20"}
	for i, id := range ids {
		tenants = append(tenants, rollupTenant(id, "s"+id, ""))
		store.signIn[id] = SignInStats{ActiveUsers: 1, LastSignInAt: &rollupDay}
		store.hourly[id] = HourlyTotals{HandlerMs: int64(i+1) * 3_600_000}
	}
	r := newTestRollup(tenants, rollupFakePools{}, store, nil, rollupFakeSource{})

	_, err := r.Run(context.Background(), rollupDay)
	require.NoError(t, err)
	var outliers []string
	for _, f := range store.firings {
		if f.RuleKey == RuleFleetOutlier {
			outliers = append(outliers, f.TenantID)
		}
	}
	assert.Equal(t, []string{"t20"}, outliers)
}

func TestComputePerUser(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]float64
		active int
		want   float64
	}{
		{"no active users", map[string]float64{MetricComputeRequestHours: 4}, 0, 0},
		{"requests and jobs", map[string]float64{MetricComputeRequestHours: 3, MetricComputeJobHours: 1}, 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, computePerUser(tt.values, tt.active))
		})
	}
}

func TestDaysSince(t *testing.T) {
	ref := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	assert.Nil(t, daysSince(nil, ref))
	old := ref.AddDate(0, 0, -15)
	require.NotNil(t, daysSince(&old, ref))
	assert.Equal(t, 15, *daysSince(&old, ref))
	future := ref.Add(time.Hour)
	assert.Equal(t, 0, *daysSince(&future, ref))
}
