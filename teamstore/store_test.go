// teamstore/store_test.go
//go:build dbtest

package teamstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// seedUser inserts a tenant user and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (identity_id, email, full_name)
		VALUES (gen_random_uuid(), $1, $2)
		RETURNING id`,
		"user-"+suffix+"@example.com", "User "+suffix).Scan(&id)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func TestCreateGetUpdateDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	team, err := CreateTeam(ctx, pool, "Sales West")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if team.Name != "Sales West" {
		t.Errorf("Name = %q, want Sales West", team.Name)
	}

	got, err := GetTeam(ctx, pool, team.ID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.MemberCount != 0 || len(got.Members) != 0 {
		t.Errorf("new team should have no members, got %d", got.MemberCount)
	}

	upd, err := UpdateTeam(ctx, pool, team.ID, "Sales East")
	if err != nil {
		t.Fatalf("UpdateTeam: %v", err)
	}
	if upd.Name != "Sales East" {
		t.Errorf("updated Name = %q, want Sales East", upd.Name)
	}

	if err := DeleteTeam(ctx, pool, team.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if _, err := GetTeam(ctx, pool, team.ID); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("GetTeam after delete = %v, want ErrTeamNotFound", err)
	}
}

func TestMembershipLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	team, err := CreateTeam(ctx, pool, "Team "+fmt.Sprint(time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	userID := seedUser(t, pool)

	if err := AddMember(ctx, pool, team.ID, userID); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	// Idempotent re-add.
	if err := AddMember(ctx, pool, team.ID, userID); err != nil {
		t.Fatalf("AddMember (repeat): %v", err)
	}

	got, err := GetTeam(ctx, pool, team.ID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.MemberCount != 1 || len(got.Members) != 1 || got.Members[0].UserID != userID {
		t.Fatalf("expected exactly one member %s, got %+v", userID, got.Members)
	}

	if err := RemoveMember(ctx, pool, team.ID, userID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	got, _ = GetTeam(ctx, pool, team.ID)
	if got.MemberCount != 0 {
		t.Errorf("MemberCount after remove = %d, want 0", got.MemberCount)
	}
}

func TestMembershipErrors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	missing := "00000000-0000-0000-0000-000000000000"

	team, _ := CreateTeam(ctx, pool, "Team "+fmt.Sprint(time.Now().UnixNano()))

	if err := AddMember(ctx, pool, missing, seedUser(t, pool)); !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("AddMember to missing team = %v, want ErrTeamNotFound", err)
	}
	if err := AddMember(ctx, pool, team.ID, missing); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("AddMember with missing user = %v, want ErrUserNotFound", err)
	}
	if err := RemoveMember(ctx, pool, missing, seedUser(t, pool)); !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("RemoveMember from missing team = %v, want ErrTeamNotFound", err)
	}
	if _, err := UpdateTeam(ctx, pool, missing, "x"); !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("UpdateTeam missing = %v, want ErrTeamNotFound", err)
	}
	if err := DeleteTeam(ctx, pool, missing); !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("DeleteTeam missing = %v, want ErrTeamNotFound", err)
	}
}
