//go:build dbtest

package feedback

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestPool connects to the control-plane-schema test database. Skips
// cleanly when TEST_CP_DATABASE_URL is unset, mirroring
// tenancy.newCPTestControlPlane's convention for the same split (control
// plane vs. per-tenant schema).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedTenant inserts a minimal tenant row and returns its id.
func seedTenant(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO tenants (slug, display_name) VALUES ($1, $2) RETURNING id`,
		name+"-"+suffix, name).Scan(&id)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

// seedIdentity inserts a minimal identity row (home tenant is only a hint,
// per the tenancy package's convention) and returns its id.
func seedIdentity(t *testing.T, pool *pgxpool.Pool, tenantID, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO identities (tenant_id, email, full_name) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, email, "Test User").Scan(&id)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	return id
}

func TestCreateAndGetForReporter_DB(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	tenantID := seedTenant(t, pool, "feedback-reporter-tenant")
	reporterID := seedIdentity(t, pool, tenantID, fmt.Sprintf("reporter-%d@example.com", time.Now().UnixNano()))

	ticket, err := Create(ctx, pool, CreateInput{
		TenantID: tenantID, ReporterIdentityID: reporterID, ReporterKind: KindStaff,
		ReporterEmail: "reporter@example.com", ReporterName: "Reporter One",
		Category: CategoryBug, Description: "Button does nothing on click.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ticket.TicketNumber == "" {
		t.Error("TicketNumber is empty, want FB-NNNNNN")
	}
	if ticket.Status != StatusNew {
		t.Errorf("Status = %q, want %q", ticket.Status, StatusNew)
	}

	got, err := GetForReporter(ctx, pool, ticket.ID, tenantID, reporterID)
	if err != nil {
		t.Fatalf("GetForReporter (owner): %v", err)
	}
	if got.ID != ticket.ID {
		t.Errorf("GetForReporter returned ticket %s, want %s", got.ID, ticket.ID)
	}
}

// TestGetForReporter_IDOR is the core security invariant: a ticket must be
// invisible to anyone but the reporter who filed it, in their own tenant —
// including another reporter in the SAME tenant, and the same reporter
// identity attempted against a DIFFERENT tenant. Both must come back
// ErrNotFound, identical to a real miss, so ticket ids cannot be enumerated.
func TestGetForReporter_IDOR(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	tenantA := seedTenant(t, pool, "feedback-idor-tenant-a")
	tenantB := seedTenant(t, pool, "feedback-idor-tenant-b")
	reporterA := seedIdentity(t, pool, tenantA, fmt.Sprintf("a-%d@example.com", time.Now().UnixNano()))
	reporterB := seedIdentity(t, pool, tenantA, fmt.Sprintf("b-%d@example.com", time.Now().UnixNano()))

	ticket, err := Create(ctx, pool, CreateInput{
		TenantID: tenantA, ReporterIdentityID: reporterA, ReporterKind: KindStaff,
		ReporterEmail: "a@example.com", ReporterName: "Reporter A",
		Category: CategoryBug, Description: "Only reporter A should see this.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("another reporter in the same tenant", func(t *testing.T) {
		_, err := GetForReporter(ctx, pool, ticket.ID, tenantA, reporterB)
		if err != ErrNotFound {
			t.Errorf("GetForReporter(other reporter) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("the owning reporter under a different tenant", func(t *testing.T) {
		_, err := GetForReporter(ctx, pool, ticket.ID, tenantB, reporterA)
		if err != ErrNotFound {
			t.Errorf("GetForReporter(wrong tenant) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("a real miss", func(t *testing.T) {
		_, err := GetForReporter(ctx, pool, "00000000-0000-0000-0000-000000000000", tenantA, reporterA)
		if err != ErrNotFound {
			t.Errorf("GetForReporter(missing id) error = %v, want ErrNotFound", err)
		}
	})
}

// TestGetForAdmin_CrossTenant confirms the admin read path is deliberately
// UNscoped by tenant or reporter — a platform admin must be able to see any
// tenant's ticket, unlike GetForReporter.
func TestGetForAdmin_CrossTenant(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	tenantID := seedTenant(t, pool, "feedback-admin-tenant")
	reporterID := seedIdentity(t, pool, tenantID, fmt.Sprintf("reporter-%d@example.com", time.Now().UnixNano()))

	ticket, err := Create(ctx, pool, CreateInput{
		TenantID: tenantID, ReporterIdentityID: reporterID, ReporterKind: KindPortal,
		ReporterEmail: "portal@example.com", ReporterName: "Portal Reporter",
		Category: CategoryFeatureRequest, Description: "Please add dark mode.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := GetForAdmin(ctx, pool, ticket.ID)
	if err != nil {
		t.Fatalf("GetForAdmin: %v", err)
	}
	if got.TenantName == "" {
		t.Error("GetForAdmin: TenantName is empty, want the joined tenant display name")
	}
	if got.ReporterKind != KindPortal {
		t.Errorf("ReporterKind = %q, want %q", got.ReporterKind, KindPortal)
	}
}

// TestUpdateStatus_AppendsTimelineEntry verifies a status change is both
// applied to the ticket and recorded as a reporter-visible timeline row.
func TestUpdateStatus_AppendsTimelineEntry(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	tenantID := seedTenant(t, pool, "feedback-status-tenant")
	reporterID := seedIdentity(t, pool, tenantID, fmt.Sprintf("reporter-%d@example.com", time.Now().UnixNano()))
	adminID := seedIdentity(t, pool, tenantID, fmt.Sprintf("admin-%d@example.com", time.Now().UnixNano()))

	ticket, err := Create(ctx, pool, CreateInput{
		TenantID: tenantID, ReporterIdentityID: reporterID, ReporterKind: KindStaff,
		ReporterEmail: "reporter@example.com", ReporterName: "Reporter",
		Category: CategoryPerformance, Description: "Page takes 10s to load.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := UpdateStatus(ctx, pool, ticket.ID, StatusInProgress, adminID, AuthorPlatformAdmin, "Admin Name"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	updated, err := GetForAdmin(ctx, pool, ticket.ID)
	if err != nil {
		t.Fatalf("GetForAdmin: %v", err)
	}
	if updated.Status != StatusInProgress {
		t.Errorf("Status = %q, want %q", updated.Status, StatusInProgress)
	}

	comments, err := ListComments(ctx, pool, ticket.ID, false)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("ListComments returned %d entries, want 1 status_change entry", len(comments))
	}
	if comments[0].EventType != EventStatusChange {
		t.Errorf("EventType = %q, want %q", comments[0].EventType, EventStatusChange)
	}
	if comments[0].OldStatus != StatusNew || comments[0].NewStatus != StatusInProgress {
		t.Errorf("status_change = %s -> %s, want %s -> %s", comments[0].OldStatus, comments[0].NewStatus, StatusNew, StatusInProgress)
	}
}

// TestListComments_ExcludesInternalForReporter is the internal-notes
// invariant: is_internal rows must never reach a reporter-facing response.
func TestListComments_ExcludesInternalForReporter(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	tenantID := seedTenant(t, pool, "feedback-internal-tenant")
	reporterID := seedIdentity(t, pool, tenantID, fmt.Sprintf("reporter-%d@example.com", time.Now().UnixNano()))
	adminID := seedIdentity(t, pool, tenantID, fmt.Sprintf("admin-%d@example.com", time.Now().UnixNano()))

	ticket, err := Create(ctx, pool, CreateInput{
		TenantID: tenantID, ReporterIdentityID: reporterID, ReporterKind: KindStaff,
		ReporterEmail: "reporter@example.com", ReporterName: "Reporter",
		Category: CategoryBug, Description: "Crash on submit.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := AddComment(ctx, pool, AddCommentInput{
		FeedbackID: ticket.ID, AuthorIdentityID: reporterID, AuthorKind: KindStaff,
		AuthorName: "Reporter", Body: "Here's a repro.", IsInternal: false,
	}); err != nil {
		t.Fatalf("AddComment (reporter): %v", err)
	}
	if _, err := AddComment(ctx, pool, AddCommentInput{
		FeedbackID: ticket.ID, AuthorIdentityID: adminID, AuthorKind: AuthorPlatformAdmin,
		AuthorName: "Admin", Body: "Escalating to eng.", IsInternal: true,
	}); err != nil {
		t.Fatalf("AddComment (internal): %v", err)
	}

	reporterView, err := ListComments(ctx, pool, ticket.ID, false)
	if err != nil {
		t.Fatalf("ListComments(includeInternal=false): %v", err)
	}
	if len(reporterView) != 1 {
		t.Fatalf("reporter view has %d comments, want 1 (internal note must be excluded)", len(reporterView))
	}
	if reporterView[0].IsInternal {
		t.Error("reporter view returned an internal comment")
	}

	adminView, err := ListComments(ctx, pool, ticket.ID, true)
	if err != nil {
		t.Fatalf("ListComments(includeInternal=true): %v", err)
	}
	if len(adminView) != 2 {
		t.Fatalf("admin view has %d comments, want 2", len(adminView))
	}
}

// TestUnreadCountForReporter_ClearedByMarkAllSeen exercises the badge
// lifecycle: a reply after the reporter's last-seen timestamp counts as
// unread, and MarkAllSeenForReporter clears it.
func TestUnreadCountForReporter_ClearedByMarkAllSeen(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	tenantID := seedTenant(t, pool, "feedback-unread-tenant")
	reporterID := seedIdentity(t, pool, tenantID, fmt.Sprintf("reporter-%d@example.com", time.Now().UnixNano()))
	adminID := seedIdentity(t, pool, tenantID, fmt.Sprintf("admin-%d@example.com", time.Now().UnixNano()))

	ticket, err := Create(ctx, pool, CreateInput{
		TenantID: tenantID, ReporterIdentityID: reporterID, ReporterKind: KindStaff,
		ReporterEmail: "reporter@example.com", ReporterName: "Reporter",
		Category: CategoryUXImprovement, Description: "The button is hard to find.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	before, err := UnreadCountForReporter(ctx, pool, tenantID, reporterID)
	if err != nil {
		t.Fatalf("UnreadCountForReporter (before reply): %v", err)
	}
	if before != 0 {
		t.Errorf("UnreadCountForReporter (before reply) = %d, want 0", before)
	}

	if _, err := AddComment(ctx, pool, AddCommentInput{
		FeedbackID: ticket.ID, AuthorIdentityID: adminID, AuthorKind: AuthorPlatformAdmin,
		AuthorName: "Admin", Body: "Thanks, looking into it.", IsInternal: false,
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	after, err := UnreadCountForReporter(ctx, pool, tenantID, reporterID)
	if err != nil {
		t.Fatalf("UnreadCountForReporter (after reply): %v", err)
	}
	if after != 1 {
		t.Errorf("UnreadCountForReporter (after reply) = %d, want 1", after)
	}

	if err := MarkAllSeenForReporter(ctx, pool, tenantID, reporterID); err != nil {
		t.Fatalf("MarkAllSeenForReporter: %v", err)
	}

	cleared, err := UnreadCountForReporter(ctx, pool, tenantID, reporterID)
	if err != nil {
		t.Fatalf("UnreadCountForReporter (after mark-seen): %v", err)
	}
	if cleared != 0 {
		t.Errorf("UnreadCountForReporter (after mark-seen) = %d, want 0", cleared)
	}
}
