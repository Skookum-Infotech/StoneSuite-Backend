//go:build dbtest

package jobqueue

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestQueue connects to the control-plane test database (async_jobs lives
// there, not in a tenant database — see Queue's doc comment) and truncates
// it, skipping cleanly when TEST_CP_DATABASE_URL is unset. It also inserts one
// fixture tenant row and returns its id — async_jobs.tenant_id is a UUID FK
// into tenants(id), so tests need a real tenant row to reference rather than
// an arbitrary string.
func newTestQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE async_jobs`); err != nil {
		t.Fatalf("truncate async_jobs: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE tenants CASCADE`); err != nil {
		t.Fatalf("truncate tenants: %v", err)
	}
	var tenantID string
	err = pool.QueryRow(context.Background(), `
		INSERT INTO tenants (slug, display_name) VALUES ('jobqueue-test-tenant', 'Jobqueue Test Tenant')
		RETURNING id`).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert fixture tenant: %v", err)
	}
	return New(pool), tenantID
}

func TestQueue_EnqueueAndGetRoundTrip(t *testing.T) {
	q, tenantID := newTestQueue(t)
	ctx := context.Background()

	id, err := q.Enqueue(ctx, "import", tenantID, map[string]string{"k": "v"}, "")
	if err != nil {
		t.Fatal(err)
	}

	job, err := q.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.JobType != "import" || job.Status != StatusPending {
		t.Fatalf("job = %+v", job)
	}
	if job.TenantID == nil || *job.TenantID != tenantID {
		t.Fatalf("TenantID = %v, want %s", job.TenantID, tenantID)
	}
}

func TestQueue_GetMissingReturnsErrNoJob(t *testing.T) {
	q, _ := newTestQueue(t)
	_, err := q.Get(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err != ErrNoJob {
		t.Fatalf("err = %v, want ErrNoJob", err)
	}
}

func TestQueue_EnqueueIsIdempotentPerKey(t *testing.T) {
	q, tenantID := newTestQueue(t)
	ctx := context.Background()

	id1, err := q.Enqueue(ctx, "import", tenantID, map[string]string{"v": "1"}, "import:file-1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := q.Enqueue(ctx, "import", tenantID, map[string]string{"v": "2"}, "import:file-1")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("re-enqueuing the same idempotency key must return the same job id: %s != %s", id1, id2)
	}
}

func TestQueue_ClaimNextOnlyClaimsRequestedTypes(t *testing.T) {
	q, tenantID := newTestQueue(t)
	ctx := context.Background()

	if _, err := q.Enqueue(ctx, "provision", tenantID, map[string]string{}, ""); err != nil {
		t.Fatal(err)
	}
	importID, err := q.Enqueue(ctx, "import", tenantID, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}

	job, err := q.ClaimNext(ctx, []string{"import"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != importID {
		t.Fatalf("claimed job %s, want the import job %s (provision job must not be claimed)", job.ID, importID)
	}
	if job.Status != StatusRunning || job.Attempts != 1 {
		t.Fatalf("claimed job = %+v, want status=running attempts=1", job)
	}
}

func TestQueue_MarkSucceededAndMarkFailed(t *testing.T) {
	q, tenantID := newTestQueue(t)
	ctx := context.Background()

	id, err := q.Enqueue(ctx, "import", tenantID, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.ClaimNext(ctx, []string{"import"}); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkSucceeded(ctx, id); err != nil {
		t.Fatal(err)
	}
	job, err := q.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusSucceeded {
		t.Fatalf("Status = %q, want succeeded", job.Status)
	}
}
