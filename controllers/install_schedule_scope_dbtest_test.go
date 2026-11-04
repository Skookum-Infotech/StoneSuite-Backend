//go:build dbtest

package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/installschedule"
)

// An own-scope crew updater who overlaps a colleague's visit they cannot see gets the conflict redacted.
func TestInstallSchedule_RedactedConflictForOwnScopeUpdater(t *testing.T) {
	e := newISEnv(t)
	owner := e.caller("redowner")
	updater := e.caller("redupdater", isGrant(authz.ResourceInstallAppointment, authz.ActionUpdate, authz.ScopeOwn))
	colleague := e.caller("redcolleague")
	_, job1 := e.job(owner.employeeID)
	_, job2 := e.job(owner.employeeID)
	mine := e.appt(job1, owner.employeeID, "06:00", "07:00", updater.employeeID, colleague.employeeID)
	e.appt(job2, owner.employeeID, "10:00", "14:00", colleague.employeeID)

	body := fmt.Sprintf(`{"expectedVersion":%d,"startLocal":"%sT11:00","endLocal":"%sT12:00"}`, mine.Version, isFarDay, isFarDay)
	rec := e.do(updater, http.MethodPatch, isBasePath+"/"+mine.ID, body)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var resp struct {
		RequiresAcknowledgement bool                       `json:"requiresAcknowledgement"`
		Conflicts               []installschedule.Conflict `json:"conflicts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.RequiresAcknowledgement)
	require.Len(t, resp.Conflicts, 1)
	assert.Equal(t, installschedule.ConflictCrew, resp.Conflicts[0].Type)
	assert.Empty(t, resp.Conflicts[0].AppointmentUUID)
	assert.Empty(t, resp.Conflicts[0].JobNumber)
	assert.NotEmpty(t, resp.Conflicts[0].EmployeeName)
}

// ListForJob under own scope returns only visits the caller owns the job of or crews.
func TestInstallSchedule_ListForJob_OwnScope(t *testing.T) {
	e := newISEnv(t)
	owner := e.caller("lfjowner",
		isGrant(authz.ResourceInstallAppointment, authz.ActionRead, authz.ScopeOwn),
		isGrant(authz.ResourceInstallation, authz.ActionRead, authz.ScopeAll))
	reader := e.caller("lfjreader",
		isGrant(authz.ResourceInstallAppointment, authz.ActionRead, authz.ScopeOwn),
		isGrant(authz.ResourceInstallation, authz.ActionRead, authz.ScopeAll))
	_, jobUUID := e.job(owner.employeeID)
	e.appt(jobUUID, owner.employeeID, "08:00", "09:00", reader.employeeID)
	e.appt(jobUUID, owner.employeeID, "10:00", "11:00")
	path := "/api/tenant/fabrication-jobs/" + jobUUID + "/install-appointments"

	count := func(c isCaller) int {
		rec := e.do(c, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp struct {
			Appointments []installschedule.Appointment `json:"appointments"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return len(resp.Appointments)
	}
	assert.Equal(t, 1, count(reader))
	assert.Equal(t, 2, count(owner))
}

// An own-scope caller who neither owns nor crews a visit cannot patch, cancel or complete it.
func TestInstallSchedule_MutationsOutOfScopeAre404(t *testing.T) {
	e := newISEnv(t)
	owner := e.caller("mutowner")
	_, jobUUID := e.job(owner.employeeID)
	appt := e.appt(jobUUID, owner.employeeID, "08:00", "12:00")
	stranger := e.caller("mutstranger",
		isGrant(authz.ResourceInstallAppointment, authz.ActionUpdate, authz.ScopeOwn),
		isGrant(authz.ResourceInstallAppointment, authz.ActionTransition, authz.ScopeOwn))
	path := isBasePath + "/" + appt.ID
	body := fmt.Sprintf(`{"expectedVersion":%d,"reason":"x","notes":"y"}`, appt.Version)

	assert.Equal(t, http.StatusNotFound, e.do(stranger, http.MethodPatch, path, body).Code)
	assert.Equal(t, http.StatusNotFound, e.do(stranger, http.MethodPost, path+"/cancel", body).Code)
	assert.Equal(t, http.StatusNotFound, e.do(stranger, http.MethodPost, path+"/complete", body).Code)
}
