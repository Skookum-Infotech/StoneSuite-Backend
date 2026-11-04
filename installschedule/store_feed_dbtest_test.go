//go:build dbtest

package installschedule

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dbtestutil"
)

const (
	scopeAllTest = "all"
	scopeOwnTest = "own"
	tzChicago    = "America/Chicago"
	statusFJOB   = `SELECT record_status_id FROM lkp_record_status s
		JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
		WHERE t.record_type_code = 'FJOB' ORDER BY record_status_id LIMIT 1`
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
	dbtestutil.EnsureLocation(t, pool)
	return pool
}

func uniq() string { return fmt.Sprintf("%d", time.Now().UnixNano()%1e12) }

// seedUserEmployee creates a users row and an employee linked to it.
func seedUserEmployee(t *testing.T, pool *pgxpool.Pool, name string) (userID, identityID string, empID int) {
	t.Helper()
	ctx := context.Background()
	email := name + uniq() + "@example.test"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, full_name) VALUES (gen_random_uuid(), $1, $2)
		 RETURNING id::text, identity_id::text`, email, name).Scan(&userID, &identityID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email)
		 VALUES ($1, $2, 'Tester', $3) RETURNING employee_id`, userID, name, email).Scan(&empID))
	return userID, identityID, empID
}

// seedJob inserts a fabrication job owned by ownerEmp and returns its id and uuid.
func seedJob(t *testing.T, pool *pgxpool.Pool, ownerEmp int) (jobID int, jobUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := uniq()
	var custType, custID, soType, soStatus, fjType, fjStatus, soID int
	require.NoError(t, pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code='CUST'`).Scan(&custType))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO customer (record_type, customer_name, customer_created_by) VALUES ($1,$2,1) RETURNING customer_id`,
		custType, "IS Customer "+suffix).Scan(&custID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code='SORD'`).Scan(&soType))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type=$1 AND record_status_code='OPEN'`, soType).Scan(&soStatus))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO sales_order (sales_order_number, record_type, sales_order_customer_id, sales_order_status, sales_order_created_by)
		 VALUES ($1,$2,$3,$4,1) RETURNING sales_order_id`, "SO-"+suffix, soType, custID, soStatus).Scan(&soID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code='FJOB'`).Scan(&fjType))
	require.NoError(t, pool.QueryRow(ctx, statusFJOB).Scan(&fjStatus))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO fabrication_job (fabrication_job_number, record_type, fabrication_job_status, sales_order_id,
		        fabrication_job_customer_id, job_owner_id, job_site_addr_city)
		 VALUES ($1,$2,$3,$4,$5,$6,'Austin') RETURNING fabrication_job_id, fabrication_job_uuid::text`,
		"FJ"+suffix, fjType, fjStatus, soID, custID, ownerEmp).Scan(&jobID, &jobUUID))
	return jobID, jobUUID
}

// seedAppt inserts an appointment (and optional crew) and returns its uuid.
func seedAppt(t *testing.T, pool *pgxpool.Pool, jobID int, start, end time.Time, cancelled bool, crew ...int) string {
	t.Helper()
	ctx := context.Background()
	status := "scheduled"
	var cancelledAt any
	if cancelled {
		status = "cancelled"
		cancelledAt = start
	}
	var id int
	var uuid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO install_appointment (fabrication_job_id, appointment_status, starts_at, ends_at,
		        local_start_date, local_end_date, timezone, cancelled_at, created_by, updated_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,1) RETURNING install_appointment_id, install_appointment_uuid::text`,
		jobID, status, start, end, start.In(chicagoLoc(t)).Format("2006-01-02"), end.In(chicagoLoc(t)).Format("2006-01-02"),
		tzChicago, cancelledAt).Scan(&id, &uuid))
	for i, e := range crew {
		role := "member"
		if i == 0 {
			role = "lead"
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by) VALUES ($1,$2,$3,1)`,
			id, e, role)
		require.NoError(t, err)
	}
	return uuid
}

func chicagoLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(tzChicago)
	require.NoError(t, err)
	return loc
}

// purgeYear deletes every appointment starting in the given year so fixed-date tests can re-run on one DB.
func purgeYear(t *testing.T, pool *pgxpool.Pool, year int) {
	t.Helper()
	const where = `install_appointment_id IN (SELECT install_appointment_id FROM install_appointment
		WHERE starts_at >= make_date($1,1,1) - interval '2 days' AND starts_at < make_date($1+1,1,1))`
	for _, table := range []string{"install_appointment_history", "install_appointment_crew", "install_appointment"} {
		_, err := pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE `+where, year)
		require.NoError(t, err)
	}
}

func TestFeed_WindowAndScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const feedYear = 2031
	purgeYear(t, pool, feedYear)
	loc := chicagoLoc(t)
	ownerUser, ownerIdent, ownerEmp := seedUserEmployee(t, pool, "owner")
	crewUser, crewIdent, crewEmp := seedUserEmployee(t, pool, "crew")
	_, strangerIdent, _ := seedUserEmployee(t, pool, "stranger")
	jobID, jobUUID := seedJob(t, pool, ownerEmp)

	in := time.Date(2031, 5, 12, 8, 0, 0, 0, loc)
	inID := seedAppt(t, pool, jobID, in, in.Add(4*time.Hour), false, crewEmp)
	out := time.Date(2031, 7, 12, 8, 0, 0, 0, loc)
	seedAppt(t, pool, jobID, out, out.Add(4*time.Hour), false)
	canc := time.Date(2031, 5, 13, 8, 0, 0, 0, loc)
	seedAppt(t, pool, jobID, canc, canc.Add(4*time.Hour), true)

	// Boundaries: window is [2031-05-01 00:00, 2031-06-01 00:00) local.
	winStart := time.Date(2031, 5, 1, 0, 0, 0, 0, loc)
	winEnd := time.Date(2031, 6, 1, 0, 0, 0, 0, loc)
	seedAppt(t, pool, jobID, winStart.Add(-2*time.Hour), winStart, false) // ends exactly at winStart
	seedAppt(t, pool, jobID, winEnd, winEnd.Add(2*time.Hour), false)      // starts exactly at winEnd

	req := FeedRequest{From: "2031-05-01", To: "2031-05-31"}

	items, truncated, err := Feed(ctx, pool, loc, scopeAllTest, "", req)
	require.NoError(t, err)
	assert.False(t, truncated)
	require.Len(t, items, 1)
	assert.Equal(t, inID, items[0].ID)
	assert.Equal(t, jobUUID, items[0].FabricationJobID)
	assert.Equal(t, "Austin", items[0].SiteCity)
	assert.Equal(t, "2031-05-12T08:00", items[0].StartLocal)
	assert.Equal(t, "2031-05-12T12:00", items[0].EndLocal)
	require.Len(t, items[0].Crew, 1)
	assert.Equal(t, crewEmp, items[0].Crew[0].EmployeeID)

	withCancelled := req
	withCancelled.IncludeCancelled = true
	items, _, err = Feed(ctx, pool, loc, scopeAllTest, "", withCancelled)
	require.NoError(t, err)
	assert.Len(t, items, 2)

	items, _, err = Feed(ctx, pool, loc, scopeOwnTest, strangerIdent, req)
	require.NoError(t, err)
	assert.Empty(t, items)

	items, _, err = Feed(ctx, pool, loc, scopeOwnTest, crewIdent, req)
	require.NoError(t, err)
	assert.Len(t, items, 1)

	items, _, err = Feed(ctx, pool, loc, scopeOwnTest, ownerIdent, req)
	require.NoError(t, err)
	assert.Len(t, items, 1)

	byEmp := req
	byEmp.EmployeeID = ownerEmp
	items, _, err = Feed(ctx, pool, loc, scopeAllTest, "", byEmp)
	require.NoError(t, err)
	assert.Empty(t, items)

	gotOwner, gotCrew, err := OwnerAndCrew(ctx, pool, inID)
	require.NoError(t, err)
	assert.Equal(t, ownerUser, gotOwner)
	assert.Equal(t, []string{crewUser}, gotCrew)

	got, err := Get(ctx, pool, inID)
	require.NoError(t, err)
	assert.Equal(t, inID, got.ID)
	_, err = Get(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)

	list, err := ListForJob(ctx, pool, jobUUID)
	require.NoError(t, err)
	assert.Len(t, list, 5)
}

func TestFeed_Truncates(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, ownerEmp := seedUserEmployee(t, pool, "trunc")
	jobID, _ := seedJob(t, pool, ownerEmp)
	for i := 0; i < 3; i++ {
		s := time.Date(2032, 5, 10+i, 8, 0, 0, 0, loc)
		seedAppt(t, pool, jobID, s, s.Add(time.Hour), false)
	}
	old := feedRowCap
	feedRowCap = 2
	t.Cleanup(func() { feedRowCap = old })

	items, truncated, err := Feed(ctx, pool, loc, scopeAllTest, "", FeedRequest{From: "2032-05-01", To: "2032-05-31"})
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, items, 2)
}

func TestCrewOptions_ExcludesSystemAndInactive(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, activeEmp := seedUserEmployee(t, pool, "active")
	_, _, inactiveEmp := seedUserEmployee(t, pool, "inactive")
	_, err := pool.Exec(ctx, `UPDATE employee SET employee_is_active = false WHERE employee_id = $1`, inactiveEmp)
	require.NoError(t, err)

	opts, err := CrewOptions(ctx, pool)
	require.NoError(t, err)
	ids := map[int]CrewOption{}
	for _, o := range opts {
		ids[o.EmployeeID] = o
	}
	assert.NotContains(t, ids, 1)
	assert.NotContains(t, ids, inactiveEmp)
	require.Contains(t, ids, activeEmp)
	assert.True(t, ids[activeEmp].HasLogin)
}

func TestGet_SoftDeletedJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	loc := chicagoLoc(t)
	_, _, ownerEmp := seedUserEmployee(t, pool, "softdel")
	jobID, _ := seedJob(t, pool, ownerEmp)
	s := time.Date(2033, 5, 10, 8, 0, 0, 0, loc)
	id := seedAppt(t, pool, jobID, s, s.Add(time.Hour), false)
	_, err := pool.Exec(ctx, `UPDATE fabrication_job SET fabrication_job_deleted_at = now(), fabrication_job_deleted_by = 1
		WHERE fabrication_job_id = $1`, jobID)
	require.NoError(t, err)

	_, err = Get(ctx, pool, id)
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = OwnerAndCrew(ctx, pool, id)
	assert.ErrorIs(t, err, ErrNotFound)
}
