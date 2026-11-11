package controllers

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/automation"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/installschedule"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// Install-schedule query parameters and resource names.
const (
	installParamFrom             = "from"
	installParamTo               = "to"
	installParamEmployee         = "employeeId"
	installParamVisitType        = "visitType"
	installParamIncludeCancelled = "includeCancelled"
	installResourceName          = string(authz.ResourceInstallAppointment)
	installSystemEmployeeID      = 1
)

// InstallScheduleOps handles the install-schedule endpoints: calendar visits (template, install,
// service) booked against fabrication jobs. Guarded by authz.ResourceInstallAppointment; creating
// a visit additionally needs installation:read on the job.
//
// Routes:
//
//	GET  /api/tenant/install-appointments                      — calendar feed (from/to window)
//	GET  /api/tenant/install-appointments/crew-options         — assignable employees
//	POST /api/tenant/install-appointments                      — book a visit
//	GET  /api/tenant/install-appointments/{uuid}               — get
//	PATCH /api/tenant/install-appointments/{uuid}              — reschedule / reassign / edit
//	POST /api/tenant/install-appointments/{uuid}/cancel        — cancel
//	POST /api/tenant/install-appointments/{uuid}/complete      — complete
//	GET  /api/tenant/install-appointments/{uuid}/history       — audit rows
//	GET  /api/tenant/fabrication-jobs/{uuid}/install-appointments — visits of one job
type InstallScheduleOps struct {
	fallbackTZ *time.Location
	now        func() time.Time
	jobs       *FabricationOps
}

// NewInstallScheduleOps constructs the handler group; fallbackTZ (nil means UTC) is used when the tenant has no valid timezone.
func NewInstallScheduleOps(fallbackTZ *time.Location) *InstallScheduleOps {
	return &InstallScheduleOps{fallbackTZ: fallbackTZ, now: time.Now, jobs: NewFabricationOps(nil)} // jobs is used only for auth; it sends no customer email, so no control plane
}

// installCaller is the authenticated caller's tenant pool, identity and granted scope.
type installCaller struct {
	pool       *pgxpool.Pool
	identityID string
	scope      authz.Scope
}

// authInstallAny resolves JWT + tenant pool and requires resource:<any of actions>, logging and answering 403 otherwise.
func (h *InstallScheduleOps) authInstallAny(w http.ResponseWriter, r *http.Request, resource authz.Resource, actions ...authz.Action) (*installCaller, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	for _, action := range actions {
		decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
		if err != nil {
			installFail(w, http.StatusInternalServerError, installCodeInternal, msgInstallPermCheck, nil)
			return nil, false
		}
		if decision.Allowed {
			return &installCaller{pool: pool, identityID: payload.ID, scope: decision.Scope}, true
		}
	}
	logSecurityEvent(r, "permission_denied",
		"identity", payload.ID, "resource", string(resource), "action", string(actions[0]))
	installFail(w, http.StatusForbidden, installCodeForbidden, "You do not have permission to "+string(actions[0])+" install appointments.", nil)
	return nil, false
}

// authInstall requires install_appointment:<action> for requests with no specific record.
func (h *InstallScheduleOps) authInstall(w http.ResponseWriter, r *http.Request, action authz.Action) (*installCaller, bool) {
	return h.authInstallAny(w, r, authz.ResourceInstallAppointment, action)
}

// inApptScope reports whether the caller may see an appointment: all scope, the job owner, or an assigned crew member.
func (c *installCaller) inApptScope(ctx context.Context, owner string, crew []string) (bool, error) {
	allowed, err := recordInScope(ctx, c.pool, c.scope, c.identityID, owner)
	if err != nil || allowed {
		return allowed, err
	}
	uid, err := workflow.UserIDByIdentity(ctx, c.pool, c.identityID)
	if err != nil || uid == "" {
		return false, nil
	}
	return slices.Contains(crew, uid), nil
}

// authAppt checks install_appointment:<action> then the row-level guard for {uuid}; denial is a 404 plus idor_denied.
func (h *InstallScheduleOps) authAppt(w http.ResponseWriter, r *http.Request, action authz.Action) (*installCaller, string, bool) {
	c, ok := h.authInstall(w, r, action)
	if !ok {
		return nil, "", false
	}
	uuid := r.PathValue("uuid")
	if !validUUID(uuid) {
		installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallNotFound, nil)
		return nil, "", false
	}
	owner, crew, err := installschedule.OwnerAndCrew(r.Context(), c.pool, uuid)
	if err != nil {
		writeInstallError(w, r, err)
		return nil, "", false
	}
	if c.scope != authz.ScopeAll {
		allowed, err := c.inApptScope(r.Context(), owner, crew)
		if err != nil {
			installFail(w, http.StatusInternalServerError, installCodeInternal, msgInstallPermCheck, nil)
			return nil, "", false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied", "identity", c.identityID, "resource", installResourceName,
				"record", uuid, "action", string(action), "scope", string(c.scope))
			installFail(w, http.StatusNotFound, installCodeNotFound, msgInstallNotFound, nil)
			return nil, "", false
		}
	}
	return c, uuid, true
}

// validUUID reports whether s parses as a UUID, so malformed ids 404 instead of reaching SQL.
func validUUID(s string) bool {
	var u pgtype.UUID
	return u.Scan(s) == nil
}

// canSee builds the conflict-redaction check: may the caller see that other appointment under their scope.
// The caller's users.id is resolved once here; the returned func queries through the transaction it is handed.
func (c *installCaller) canSee(ctx context.Context) installschedule.CanSeeFunc {
	if c.scope == authz.ScopeAll {
		return func(context.Context, installschedule.Querier, string) bool { return true }
	}
	uid, err := workflow.UserIDByIdentity(ctx, c.pool, c.identityID)
	if err != nil {
		slog.WarnContext(ctx, "install schedule: caller user not resolved for visibility checks", "error", err)
		uid = ""
	}
	cache := map[string]bool{}
	return func(ctx context.Context, q installschedule.Querier, uuid string) bool {
		if uid == "" {
			return false
		}
		if v, ok := cache[uuid]; ok {
			return v
		}
		owner, crew, err := installschedule.OwnerAndCrew(ctx, q, uuid)
		if err != nil {
			slog.WarnContext(ctx, "install schedule: visibility check failed", "error", err)
			return false
		}
		visible := (owner != "" && owner == uid) || slices.Contains(crew, uid)
		cache[uuid] = visible
		return visible
	}
}

// location resolves the tenant's zone from the company profile, falling back to the injected zone.
func (h *InstallScheduleOps) location(ctx context.Context, pool *pgxpool.Pool) *time.Location {
	tz := ""
	prof, err := companyprofile.Get(ctx, pool)
	if err != nil {
		slog.WarnContext(ctx, "install schedule: company profile unavailable, using fallback timezone", "error", err)
	} else if prof != nil {
		tz = prof.Timezone
	}
	return automation.TenantLocation(tz, h.fallbackTZ)
}

// actorIDs resolves the caller's employee id (system employee when unresolved) and tenant users.id.
func (h *InstallScheduleOps) actorIDs(r *http.Request, c *installCaller) (int, string) {
	empID := resolveEmployeeID(r, c.identityID)
	if empID == 0 {
		slog.WarnContext(r.Context(), "install schedule: actor employee not resolved; attributing history to system employee", "identity", c.identityID)
		empID = installSystemEmployeeID
	}
	userID, err := workflow.UserIDByIdentity(r.Context(), c.pool, c.identityID)
	if err != nil {
		slog.WarnContext(r.Context(), "install schedule: actor user not resolved", "error", err)
	}
	return empID, userID
}

// Feed GET /api/tenant/install-appointments
func (h *InstallScheduleOps) Feed(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authInstall(w, r, authz.ActionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	req := installschedule.FeedRequest{
		From: q.Get(installParamFrom), To: q.Get(installParamTo),
		VisitType:        installschedule.VisitType(q.Get(installParamVisitType)),
		IncludeCancelled: q.Get(installParamIncludeCancelled) == "true",
	}
	if s := q.Get(installParamEmployee); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			installFail(w, http.StatusBadRequest, installCodeInvalidInput, "Invalid employeeId: must be a positive integer.", nil)
			return
		}
		req.EmployeeID = n
	}
	switch req.VisitType {
	case "", installschedule.VisitTemplate, installschedule.VisitInstall, installschedule.VisitService:
	default:
		installFail(w, http.StatusBadRequest, installCodeInvalidInput, "Invalid visitType: must be template, install or service.", nil)
		return
	}
	loc := h.location(r.Context(), c.pool)
	items, truncated, err := installschedule.Feed(r.Context(), c.pool, loc, string(c.scope), c.identityID, req)
	if err != nil {
		writeInstallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "appointments": items, "truncated": truncated, "timezone": loc.String(),
	})
}

// CrewOptions GET /api/tenant/install-appointments/crew-options
func (h *InstallScheduleOps) CrewOptions(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authInstallAny(w, r, authz.ResourceInstallAppointment, authz.ActionCreate, authz.ActionUpdate)
	if !ok {
		return
	}
	opts, err := installschedule.CrewOptions(r.Context(), c.pool)
	if err != nil {
		writeInstallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "crew": opts})
}

// Get GET /api/tenant/install-appointments/{uuid}
func (h *InstallScheduleOps) Get(w http.ResponseWriter, r *http.Request) {
	c, uuid, ok := h.authAppt(w, r, authz.ActionRead)
	if !ok {
		return
	}
	appt, err := installschedule.Get(r.Context(), c.pool, uuid)
	if err != nil {
		writeInstallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "appointment": appt})
}

// History GET /api/tenant/install-appointments/{uuid}/history
func (h *InstallScheduleOps) History(w http.ResponseWriter, r *http.Request) {
	c, uuid, ok := h.authAppt(w, r, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := installschedule.History(r.Context(), c.pool, uuid)
	if err != nil {
		writeInstallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "history": rows})
}
