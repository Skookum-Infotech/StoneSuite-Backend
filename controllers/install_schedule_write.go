package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
	"stonesuite-backend/installschedule"
	"stonesuite-backend/workflow"
)

// installCrewBody is one crew assignment in a request body.
type installCrewBody struct {
	EmployeeID int    `json:"employeeId"`
	Role       string `json:"role"`
}

// installCreateBody is the POST body for booking a visit.
type installCreateBody struct {
	FabricationJobID     string            `json:"fabricationJobId"`
	VisitType            string            `json:"visitType"`
	StartLocal           string            `json:"startLocal"`
	EndLocal             string            `json:"endLocal"`
	AllDay               bool              `json:"allDay"`
	Crew                 []installCrewBody `json:"crew"`
	Notes                string            `json:"notes"`
	RequestID            string            `json:"requestId"`
	AcknowledgeConflicts bool              `json:"acknowledgeConflicts"`
}

// installUpdateBody is the PATCH body; omitted fields stay unchanged.
type installUpdateBody struct {
	StartLocal           *string            `json:"startLocal"`
	EndLocal             *string            `json:"endLocal"`
	AllDay               *bool              `json:"allDay"`
	Crew                 *[]installCrewBody `json:"crew"`
	Notes                *string            `json:"notes"`
	VisitType            *string            `json:"visitType"`
	ExpectedVersion      int                `json:"expectedVersion"`
	AcknowledgeConflicts bool               `json:"acknowledgeConflicts"`
	Reason               string             `json:"reason"`
}

// installStatusBody is the body of cancel and complete.
type installStatusBody struct {
	ExpectedVersion int    `json:"expectedVersion"`
	Reason          string `json:"reason"`
}

// toCrewInputs converts request crew to store input.
func toCrewInputs(in []installCrewBody) []installschedule.CrewInput {
	out := make([]installschedule.CrewInput, len(in))
	for i, c := range in {
		out[i] = installschedule.CrewInput{EmployeeID: c.EmployeeID, Role: installschedule.CrewRole(c.Role)}
	}
	return out
}

// decodeInstallBody reads a size-capped JSON body into dst, answering 400 and returning false on failure.
func decodeInstallBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit)).Decode(dst)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		installFail(w, http.StatusBadRequest, installCodeInvalidInput, msgInstallBodyTooLarge, nil)
	} else {
		installFail(w, http.StatusBadRequest, installCodeInvalidInput, msgInstallBadBody, nil)
	}
	return false
}

// requireVersion rejects a missing expectedVersion (versions start at 1).
func requireVersion(w http.ResponseWriter, v int) bool {
	if v >= 1 {
		return true
	}
	installFail(w, http.StatusBadRequest, installCodeInvalidInput, "Invalid expectedVersion: required.", map[string]any{"field": "expectedVersion"})
	return false
}

// authJobForCreate is the second-resource check for booking: installation:read plus job scope. Denial is 404 for out-of-scope jobs.
func (h *InstallScheduleOps) authJobForCreate(w http.ResponseWriter, r *http.Request, c *installCaller, jobUUID string) bool {
	if jobUUID == "" {
		return true // the store rejects a blank job id with its own validation message
	}
	decision, err := authz.Check(r.Context(), c.pool, c.identityID, authz.ResourceInstallation, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied", "identity", c.identityID,
			"resource", string(authz.ResourceInstallation), "action", string(authz.ActionRead),
			"context", "install_appointment_create", "source_record", jobUUID)
		installFail(w, http.StatusForbidden, installCodeForbidden, "You do not have permission to read fabrication jobs.", nil)
		return false
	}
	if !validUUID(jobUUID) {
		installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallJobNotFound, nil)
		return false
	}
	job, err := fabrication.Get(r.Context(), c.pool, jobUUID)
	if errors.Is(err, fabrication.ErrNotFound) {
		installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallJobNotFound, nil)
		return false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "install schedule: load job for create failed", "error", err)
		installFail(w, http.StatusInternalServerError, installCodeInternal, msgInstallInternal, nil)
		return false
	}
	if decision.Scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), c.pool, decision.Scope, c.identityID, job.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied", "identity", c.identityID, "record", jobUUID,
				"resource", string(authz.ResourceInstallation), "action", string(authz.ActionRead),
				"scope", string(decision.Scope), "context", "install_appointment_create")
			installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallJobNotFound, nil)
			return false
		}
	}
	return true
}

// Create POST /api/tenant/install-appointments
func (h *InstallScheduleOps) Create(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authInstall(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var body installCreateBody
	if !decodeInstallBody(w, r, &body) {
		return
	}
	if !h.authJobForCreate(w, r, c, body.FabricationJobID) {
		return
	}
	empID, userID := h.actorIDs(r, c)
	in := installschedule.CreateInput{
		FabricationJobUUID: body.FabricationJobID, VisitType: installschedule.VisitType(body.VisitType),
		StartLocal: body.StartLocal, EndLocal: body.EndLocal, AllDay: body.AllDay,
		Crew: toCrewInputs(body.Crew), Notes: body.Notes, RequestID: body.RequestID,
		AcknowledgeConflicts: body.AcknowledgeConflicts, ActorUserID: userID,
	}
	loc := h.location(r.Context(), c.pool)
	res, err := installschedule.Create(r.Context(), c.pool, loc, h.now(), in, empID, c.canSee(r.Context()))
	if err != nil {
		writeInstallError(w, err)
		return
	}
	status := http.StatusCreated
	emailSent := false
	if res.Replayed {
		status = http.StatusOK
	} else {
		h.auditInstall(r, c, userID, installschedule.ActionCreate, nil, res.Appointment)
		emailSent, _ = installschedule.NotifyCreate(r.Context(), c.pool, res, c.identityID, empID)
	}
	writeJSON(w, status, map[string]any{"success": true, "appointment": res.Appointment, "emailSent": emailSent})
}

// Update PATCH /api/tenant/install-appointments/{uuid}
func (h *InstallScheduleOps) Update(w http.ResponseWriter, r *http.Request) {
	c, uuid, ok := h.authAppt(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body installUpdateBody
	if !decodeInstallBody(w, r, &body) || !requireVersion(w, body.ExpectedVersion) {
		return
	}
	empID, userID := h.actorIDs(r, c)
	in := installschedule.UpdateInput{
		StartLocal: body.StartLocal, EndLocal: body.EndLocal, AllDay: body.AllDay, Notes: body.Notes,
		ExpectedVersion: body.ExpectedVersion, AcknowledgeConflicts: body.AcknowledgeConflicts,
		Reason: body.Reason, ActorUserID: userID,
	}
	if body.Crew != nil {
		crew := toCrewInputs(*body.Crew)
		in.Crew = &crew
	}
	if body.VisitType != nil {
		vt := installschedule.VisitType(*body.VisitType)
		in.VisitType = &vt
	}
	loc := h.location(r.Context(), c.pool)
	res, err := installschedule.Update(r.Context(), c.pool, loc, h.now(), uuid, in, empID, c.canSee(r.Context()))
	if err != nil {
		writeInstallError(w, err)
		return
	}
	h.finishChange(w, r, c, userID, empID, res)
}

// Cancel POST /api/tenant/install-appointments/{uuid}/cancel
func (h *InstallScheduleOps) Cancel(w http.ResponseWriter, r *http.Request) {
	c, uuid, ok := h.authAppt(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	var body installStatusBody
	if !decodeInstallBody(w, r, &body) || !requireVersion(w, body.ExpectedVersion) {
		return
	}
	empID, userID := h.actorIDs(r, c)
	res, err := installschedule.Cancel(r.Context(), c.pool, uuid, body.ExpectedVersion, body.Reason, empID, userID)
	if err != nil {
		writeInstallError(w, err)
		return
	}
	h.finishChange(w, r, c, userID, empID, res)
}

// Complete POST /api/tenant/install-appointments/{uuid}/complete
func (h *InstallScheduleOps) Complete(w http.ResponseWriter, r *http.Request) {
	c, uuid, ok := h.authAppt(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	var body installStatusBody
	if !decodeInstallBody(w, r, &body) || !requireVersion(w, body.ExpectedVersion) {
		return
	}
	empID, userID := h.actorIDs(r, c)
	res, err := installschedule.Complete(r.Context(), c.pool, uuid, body.ExpectedVersion, empID, h.now(), userID)
	if err != nil {
		writeInstallError(w, err)
		return
	}
	h.finishChange(w, r, c, userID, empID, res)
}

// finishChange audits each recorded action after commit, notifies the crew, and writes the success response.
func (h *InstallScheduleOps) finishChange(w http.ResponseWriter, r *http.Request, c *installCaller, userID string, empID int, res *installschedule.ChangeResult) {
	for _, action := range res.Actions {
		h.auditInstall(r, c, userID, action, res.Before, res.After)
	}
	emailSent, _ := installschedule.NotifyChange(r.Context(), c.pool, res, c.identityID, empID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "appointment": res.After, "emailSent": emailSent})
}

// installAuditSnapshot flattens an appointment for the audit trail (ids and schedule only).
func installAuditSnapshot(a *installschedule.Appointment) map[string]any {
	if a == nil {
		return nil
	}
	crew := make([]int, len(a.Crew))
	for i, m := range a.Crew {
		crew[i] = m.EmployeeID
	}
	return map[string]any{
		"id": a.ID, "fabricationJobId": a.FabricationJobID, "visitType": a.VisitType, "status": a.Status,
		"startsAt": a.StartsAt, "endsAt": a.EndsAt, "timezone": a.Timezone, "allDay": a.AllDay,
		"crewEmployeeIds": crew, "version": a.Version,
	}
}

// auditInstall records a committed write in audit_logs; a failure is logged, never returned.
func (h *InstallScheduleOps) auditInstall(r *http.Request, c *installCaller, userID, action string, before, after *installschedule.Appointment) {
	rec := after
	if rec == nil {
		rec = before
	}
	if rec == nil {
		return
	}
	if err := workflow.LogAuditFull(r.Context(), c.pool, userID, action, installResourceName, rec.ID, installResourceName,
		installAuditSnapshot(before), installAuditSnapshot(after), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		slog.WarnContext(r.Context(), "install schedule: audit write failed", "action", action, "recordId", rec.ID, "error", err)
	}
}
