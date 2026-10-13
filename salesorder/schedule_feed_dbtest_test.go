//go:build dbtest

package salesorder

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// feedFor reads one window of the calendar feed and keeps only the events of
// the given orders, so rows other tests left on a shared database don't matter.
func feedFor(t *testing.T, pool *pgxpool.Pool, scope, identity, group, from, to string, orderIDs ...string) []ScheduleEvent {
	t.Helper()
	res, err := ScheduleFeed(context.Background(), pool, scope, identity, group, from, to)
	if err != nil {
		t.Fatalf("ScheduleFeed(%s, %s..%s): %v", group, from, to, err)
	}
	want := map[string]bool{}
	for _, id := range orderIDs {
		want[id] = true
	}
	var out []ScheduleEvent
	for _, e := range res.Events {
		if len(want) == 0 || want[e.Order.ID] {
			out = append(out, e)
		}
	}
	return out
}

// seedIdentityEmployee inserts a user-linked employee and returns the
// identity id the RBAC scope resolves back to that employee.
func seedIdentityEmployee(t *testing.T, pool *pgxpool.Pool, tag string) (identityID string, employeeID int) {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("feed-%s-%d@example.test", tag, time.Now().UnixNano())
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, 'Feed Planner', 'active') RETURNING id, identity_id::text`, email).Scan(&userID, &identityID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1, 'Feed', 'Planner', $2, 1) RETURNING employee_id`, userID, email).Scan(&employeeID); err != nil {
		t.Fatalf("seed employee: %v", err)
	}
	return identityID, employeeID
}

// orderOwnedBy creates a scheduled order owned by the given employee.
func orderOwnedBy(t *testing.T, pool *pgxpool.Pool, installDate string, ownerEmployeeID int) *Order {
	t.Helper()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	o, err := Create(context.Background(), pool, CreateOrderInput{
		CustomerUUID: custUUID,
		orderFields: orderFields{
			InstallDate: installDate,
			Items:       []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
		},
	}, ownerEmployeeID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return o
}

func TestScheduleFeed_ListsOneGroupWithinTheWindow(t *testing.T) {
	pool := testPool(t)
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")
	own := scheduleByCode(t, pool, o.ID)

	events := feedFor(t, pool, "all", "", "Installation", "2026-10-12", "2026-11-10", o.ID)
	wantCodes := []string{"installation_ready", "installation_picked", "installation"}
	if len(events) != len(wantCodes) {
		t.Fatalf("got %d Installation events, want %d", len(events), len(wantCodes))
	}
	for i, e := range events {
		if e.Code != wantCodes[i] {
			t.Errorf("event %d = %s, want %s", i, e.Code, wantCodes[i])
		}
		row := own[e.Code]
		if e.ScheduledAt != row.ScheduledAt || e.Name != row.Name || e.Status != row.Status {
			t.Errorf("%s = %+v, want the order's own row %+v", e.Code, e.ScheduleActivity, row)
		}
		if e.Group != "Installation" {
			t.Errorf("%s group = %q, want Installation", e.Code, e.Group)
		}
		if e.Order.ID != o.ID || e.Order.Number != o.Number || e.Order.InstallDate != "2026-11-10" || e.Order.StatusCode != "DRFT" {
			t.Errorf("%s order = %+v, want id %s number %s install 2026-11-10 DRFT", e.Code, e.Order, o.ID, o.Number)
		}
		if e.Order.CustomerName == "" {
			t.Errorf("%s has no customer name", e.Code)
		}
	}

	t.Run("another group lists only its own activities", func(t *testing.T) {
		saw := feedFor(t, pool, "all", "", "Saw", "2026-10-12", "2026-11-10", o.ID)
		if len(saw) != 1 {
			t.Fatalf("got %d Saw events, want 1", len(saw))
		}
		for _, e := range saw {
			if e.Group != "Saw" {
				t.Errorf("event %s is in group %q, want Saw", e.Code, e.Group)
			}
		}
	})

	t.Run("a one-day window keeps only that day, both ends inclusive", func(t *testing.T) {
		day := own["installation_picked"].ScheduledAt[:10]
		got := feedFor(t, pool, "all", "", "Installation", day, day, o.ID)
		if len(got) == 0 {
			t.Fatalf("no Installation events on %s, want at least installation_picked", day)
		}
		for _, e := range got {
			if e.ScheduledAt[:10] != day {
				t.Errorf("event %s is on %s, outside the %s window", e.Code, e.ScheduledAt[:10], day)
			}
		}
	})

	t.Run("a window before the plan is empty", func(t *testing.T) {
		if got := feedFor(t, pool, "all", "", "Installation", "2026-09-01", "2026-09-30", o.ID); len(got) != 0 {
			t.Errorf("got %d events before the order existed, want 0", len(got))
		}
	})
}

func TestScheduleFeed_LeavesOutCancelledOrders(t *testing.T) {
	pool := testPool(t)
	freezeScheduleClock(t, "2026-10-12T12:00")
	live, _ := plannedOrder(t, pool, "2026-11-10")
	cancelled, _ := plannedOrder(t, pool, "2026-11-10")
	if _, err := Transition(context.Background(), pool, cancelled.ID, "CANC", 1); err != nil {
		t.Fatalf("cancel order: %v", err)
	}

	events := feedFor(t, pool, "all", "", "Installation", "2026-10-12", "2026-11-10", live.ID, cancelled.ID)
	if len(events) != 3 {
		t.Fatalf("got %d events, want only the live order's 3", len(events))
	}
	for _, e := range events {
		if e.Order.ID != live.ID {
			t.Errorf("event %s belongs to order %s, want the live order %s", e.Code, e.Order.ID, live.ID)
		}
	}
}

func TestScheduleFeed_OwnScopeSeesOnlyTheCallersOrders(t *testing.T) {
	pool := testPool(t)
	freezeScheduleClock(t, "2026-10-12T12:00")
	identityA, empA := seedIdentityEmployee(t, pool, "a")
	identityB, _ := seedIdentityEmployee(t, pool, "b")
	mine := orderOwnedBy(t, pool, "2026-11-10", empA)
	other, _ := plannedOrder(t, pool, "2026-11-10") // owned by employee 1

	t.Run("own scope", func(t *testing.T) {
		got := feedFor(t, pool, "own", identityA, "Installation", "2026-10-12", "2026-11-10", mine.ID, other.ID)
		if len(got) != 3 {
			t.Fatalf("got %d events, want the caller's 3", len(got))
		}
		for _, e := range got {
			if e.Order.ID != mine.ID {
				t.Errorf("event %s is order %s, want only %s", e.Code, e.Order.ID, mine.ID)
			}
		}
	})
	t.Run("a caller who owns nothing sees nothing", func(t *testing.T) {
		if got := feedFor(t, pool, "own", identityB, "Installation", "2026-10-12", "2026-11-10", mine.ID, other.ID); len(got) != 0 {
			t.Errorf("got %d events, want 0", len(got))
		}
	})
	t.Run("an identity with no employee sees nothing", func(t *testing.T) {
		if got := feedFor(t, pool, "own", "00000000-0000-0000-0000-000000000000", "Installation", "2026-10-12", "2026-11-10"); len(got) != 0 {
			t.Errorf("got %d events, want 0", len(got))
		}
	})
	t.Run("all scope sees every order", func(t *testing.T) {
		if got := feedFor(t, pool, "all", "", "Installation", "2026-10-12", "2026-11-10", mine.ID, other.ID); len(got) != 6 {
			t.Errorf("got %d events, want 6", len(got))
		}
	})
}

func TestScheduleFeed_RefusesBadRequests(t *testing.T) {
	pool := testPool(t)
	cases := []struct {
		name, group, from, to string
	}{
		{"unknown group", "Invoicing", "2026-10-12", "2026-10-18"},
		{"blank group", "", "2026-10-12", "2026-10-18"},
		{"reversed window", "Template", "2026-10-18", "2026-10-12"},
		{"window too long", "Template", "2026-01-01", "2026-12-31"},
		{"missing dates", "Template", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ScheduleFeed(context.Background(), pool, "all", "", tc.group, tc.from, tc.to)
			if !IsClientError(err) {
				t.Errorf("error = %v, want a client error", err)
			}
		})
	}
}
