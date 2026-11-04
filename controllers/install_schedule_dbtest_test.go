//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/installschedule"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

const (
	isBasePath  = "/api/tenant/install-appointments"
	isTestZone  = "America/Chicago"
	isFarDay    = "2033-03-15"
	isUniqueMod = 1_000_000_000_000
)

// isEnv is one seeded tenant plus the router the install-schedule routes are served from.
type isEnv struct {
	t        *testing.T
	pool     *pgxpool.Pool
	cp       *tenancy.ControlPlane
	tenantID string
	handler  http.Handler
}

// isCaller is a seeded identity with its tenant user and linked employee.
type isCaller struct {
	identityID, userID string
	employeeID         int
}

func newISEnv(t *testing.T) *isEnv {
	t.Helper()
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	loc, err := time.LoadLocation(isTestZone)
	require.NoError(t, err)
	ops := NewInstallScheduleOps(loc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+isBasePath, ops.Feed)
	mux.HandleFunc("GET "+isBasePath+"/crew-options", ops.CrewOptions)
	mux.HandleFunc("POST "+isBasePath, ops.Create)
	mux.HandleFunc("GET "+isBasePath+"/{uuid}", ops.Get)
	mux.HandleFunc("PATCH "+isBasePath+"/{uuid}", ops.Update)
	mux.HandleFunc("POST "+isBasePath+"/{uuid}/cancel", ops.Cancel)
	mux.HandleFunc("POST "+isBasePath+"/{uuid}/complete", ops.Complete)
	mux.HandleFunc("GET "+isBasePath+"/{uuid}/history", ops.History)
	mux.HandleFunc("GET /api/tenant/fabrication-jobs/{uuid}/install-appointments", ops.ListForJob)
	return &isEnv{t: t, pool: pool, cp: cp, tenantID: tenant.ID,
		handler: tenancy.NewResolver(cp, tenancy.NewRouter(nil)).Middleware(mux)}
}

// caller seeds an identity holding grants, linked to a fresh employee.
func (e *isEnv) caller(name string, grants ...authz.Grant) isCaller {
	e.t.Helper()
	ctx := context.Background()
	identity, user := seedRBACTestIdentity(e.t, e.cp, e.pool, e.tenantID, "pw")
	if len(grants) > 0 {
		unique := fmt.Sprintf("is-%s-%d", name, time.Now().UnixNano())
		roleID, err := authz.CreateRole(ctx, e.pool, unique, unique, "", grants)
		require.NoError(e.t, err)
		require.NoError(e.t, authz.AssignRole(ctx, e.pool, user.ID, roleID))
		e.t.Cleanup(func() {
			_, _ = e.pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1::uuid`, user.ID)
			_, _ = e.pool.Exec(ctx, `DELETE FROM roles WHERE id = $1::uuid`, roleID)
		})
	}
	var emp int
	require.NoError(e.t, e.pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email)
		VALUES ($1::uuid, $2, 'ISTest', $3) RETURNING employee_id`,
		user.ID, name, user.Email).Scan(&emp))
	return isCaller{identityID: identity.ID, userID: user.ID, employeeID: emp}
}

// job seeds a fabrication job owned by ownerEmp and returns its id and uuid.
func (e *isEnv) job(ownerEmp int) (int, string) {
	e.t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%isUniqueMod)
	var custType, custID, soType, soStatus, fjType, fjStatus, soID, jobID int
	var jobUUID string
	q := func(sql string, dst any, args ...any) {
		require.NoError(e.t, e.pool.QueryRow(ctx, sql, args...).Scan(dst))
	}
	q(`SELECT record_type_id FROM lkp_record_type WHERE record_type_code='CUST'`, &custType)
	q(`INSERT INTO customer (record_type, customer_name, customer_created_by) VALUES ($1,$2,1) RETURNING customer_id`,
		&custID, custType, "IS Customer "+suffix)
	q(`SELECT record_type_id FROM lkp_record_type WHERE record_type_code='SORD'`, &soType)
	q(`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type=$1 AND record_status_code='OPEN'`, &soStatus, soType)
	q(`INSERT INTO sales_order (sales_order_number, record_type, sales_order_customer_id, sales_order_status, sales_order_created_by)
	   VALUES ($1,$2,$3,$4,1) RETURNING sales_order_id`, &soID, "SO-"+suffix, soType, custID, soStatus)
	q(`SELECT record_type_id FROM lkp_record_type WHERE record_type_code='FJOB'`, &fjType)
	q(`SELECT s.record_status_id FROM lkp_record_status s JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
	   WHERE t.record_type_code = 'FJOB' ORDER BY s.record_status_id LIMIT 1`, &fjStatus)
	q(`INSERT INTO fabrication_job (fabrication_job_number, record_type, fabrication_job_status, sales_order_id,
	          fabrication_job_customer_id, job_owner_id, job_site_addr_city)
	   VALUES ($1,$2,$3,$4,$5,$6,'Austin') RETURNING fabrication_job_id`, &jobID, "FJ"+suffix, fjType, fjStatus, soID, custID, ownerEmp)
	q(`SELECT fabrication_job_uuid::text FROM fabrication_job WHERE fabrication_job_id=$1`, &jobUUID, jobID)
	return jobID, jobUUID
}

// appt books a visit through the store (all-visible) and returns it.
func (e *isEnv) appt(jobUUID string, actorEmp int, start, end string, crew ...int) *installschedule.Appointment {
	e.t.Helper()
	loc, err := time.LoadLocation(isTestZone)
	require.NoError(e.t, err)
	in := installschedule.CreateInput{
		FabricationJobUUID: jobUUID, VisitType: installschedule.VisitInstall,
		StartLocal: isFarDay + "T" + start, EndLocal: isFarDay + "T" + end,
	}
	for i, c := range crew {
		role := installschedule.CrewMemberRole
		if i == 0 {
			role = installschedule.CrewLead
		}
		in.Crew = append(in.Crew, installschedule.CrewInput{EmployeeID: c, Role: role})
	}
	res, err := installschedule.Create(context.Background(), e.pool, loc, time.Now(), in, actorEmp, func(string) bool { return true })
	require.NoError(e.t, err)
	return res.Appointment
}

// do sends a request through the tenant resolver as the caller.
func (e *isEnv) do(c isCaller, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	payload := middleware.UserContextPayload{ID: c.identityID, TenantID: e.tenantID, UserID: c.userID}
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func isGrant(res authz.Resource, action authz.Action, scope authz.Scope) authz.Grant {
	return authz.Grant{Resource: res, Action: action, Scope: scope}
}

func TestInstallSchedule_IDOR(t *testing.T) {
	e := newISEnv(t)
	owner := e.caller("owner")
	_, jobUUID := e.job(owner.employeeID)
	appt := e.appt(jobUUID, owner.employeeID, "08:00", "12:00")
	path := isBasePath + "/" + appt.ID

	reader := e.caller("reader", isGrant(authz.ResourceInstallAppointment, authz.ActionRead, authz.ScopeOwn))

	t.Run("a own-scope non-owner non-crew gets 404 on get and history", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, e.do(reader, http.MethodGet, path, "").Code)
		assert.Equal(t, http.StatusNotFound, e.do(reader, http.MethodGet, path+"/history", "").Code)
	})

	t.Run("b the same identity as crew gets 200", func(t *testing.T) {
		_, err := e.pool.Exec(context.Background(), `
			INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by)
			SELECT install_appointment_id, $2, 'member', 1 FROM install_appointment WHERE install_appointment_uuid = $1::uuid`,
			appt.ID, reader.employeeID)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, e.do(reader, http.MethodGet, path, "").Code)
		rec := e.do(reader, http.MethodGet, path+"/history", "")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("c no grant gets 403", func(t *testing.T) {
		nobody := e.caller("nobody")
		assert.Equal(t, http.StatusForbidden, e.do(nobody, http.MethodGet, path, "").Code)
		assert.Equal(t, http.StatusForbidden, e.do(nobody, http.MethodGet, path+"/history", "").Code)
	})

	t.Run("d create on a job the caller cannot see gets 404", func(t *testing.T) {
		creator := e.caller("creator",
			isGrant(authz.ResourceInstallAppointment, authz.ActionCreate, authz.ScopeAll),
			isGrant(authz.ResourceInstallation, authz.ActionRead, authz.ScopeOwn))
		body := fmt.Sprintf(`{"fabricationJobId":%q,"visitType":"install","startLocal":"%sT08:00","endLocal":"%sT12:00","crew":[]}`,
			jobUUID, isFarDay, isFarDay)
		assert.Equal(t, http.StatusNotFound, e.do(creator, http.MethodPost, isBasePath, body).Code)

		noJobRead := e.caller("nojobread", isGrant(authz.ResourceInstallAppointment, authz.ActionCreate, authz.ScopeAll))
		assert.Equal(t, http.StatusForbidden, e.do(noJobRead, http.MethodPost, isBasePath, body).Code)
	})

	t.Run("d2 create on a visible job succeeds, then cancel with a stale version is 409", func(t *testing.T) {
		creator := e.caller("creatorall",
			isGrant(authz.ResourceInstallAppointment, authz.ActionCreate, authz.ScopeAll),
			isGrant(authz.ResourceInstallAppointment, authz.ActionTransition, authz.ScopeAll),
			isGrant(authz.ResourceInstallation, authz.ActionRead, authz.ScopeAll))
		_, freshJob := e.job(owner.employeeID)
		body := fmt.Sprintf(`{"fabricationJobId":%q,"visitType":"install","startLocal":"%sT08:00","endLocal":"%sT12:00","crew":[]}`,
			freshJob, isFarDay, isFarDay)
		rec := e.do(creator, http.MethodPost, isBasePath, body)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var created struct {
			Appointment installschedule.Appointment `json:"appointment"`
			EmailSent   *bool                       `json:"emailSent"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
		require.NotNil(t, created.EmailSent)

		cancelPath := isBasePath + "/" + created.Appointment.ID + "/cancel"
		stale := e.do(creator, http.MethodPost, cancelPath, `{"expectedVersion":99,"reason":"rain"}`)
		require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
		assert.Contains(t, stale.Body.String(), `"currentVersion"`)
		ok := e.do(creator, http.MethodPost, cancelPath, fmt.Sprintf(`{"expectedVersion":%d,"reason":"rain"}`, created.Appointment.Version))
		assert.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	})

	t.Run("e patch overlap gets 409 requiring acknowledgement", func(t *testing.T) {
		updater := e.caller("updater", isGrant(authz.ResourceInstallAppointment, authz.ActionUpdate, authz.ScopeAll))
		shared := e.caller("shared").employeeID
		_, job1 := e.job(owner.employeeID)
		_, job2 := e.job(owner.employeeID)
		e.appt(job1, owner.employeeID, "08:00", "12:00", shared)
		second := e.appt(job2, owner.employeeID, "13:00", "17:00", shared)
		body := fmt.Sprintf(`{"expectedVersion":%d,"startLocal":"%sT10:00","endLocal":"%sT14:00"}`, second.Version, isFarDay, isFarDay)
		rec := e.do(updater, http.MethodPatch, isBasePath+"/"+second.ID, body)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		var resp struct {
			Success                 bool                       `json:"success"`
			Code                    string                     `json:"code"`
			RequiresAcknowledgement bool                       `json:"requiresAcknowledgement"`
			Conflicts               []installschedule.Conflict `json:"conflicts"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.False(t, resp.Success)
		assert.Equal(t, "conflict", resp.Code)
		assert.True(t, resp.RequiresAcknowledgement)
		assert.NotEmpty(t, resp.Conflicts)
	})
}

func TestInstallSchedule_Feed_BadWindow(t *testing.T) {
	e := newISEnv(t)
	reader := e.caller("feedreader", isGrant(authz.ResourceInstallAppointment, authz.ActionRead, authz.ScopeAll))

	tests := []struct {
		name, query string
		want        int
	}{
		{"to before from", "from=2033-03-10&to=2033-03-01", http.StatusBadRequest},
		{"94 day window", "from=2033-01-01&to=2033-04-04", http.StatusBadRequest},
		{"93 day window", "from=2033-01-01&to=2033-04-03", http.StatusOK},
		{"missing dates", "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(reader, http.MethodGet, isBasePath+"?"+tt.query, "")
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusBadRequest {
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}
