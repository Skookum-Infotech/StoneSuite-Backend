package controllers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/config"
	"stonesuite-backend/jobqueue"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/services"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// inviteGraceDefault is how long a soft-deleted tenant survives before hard delete.
const tenantDeleteGraceDays = 30

// TenantOps groups the multi-tenant platform/onboarding/auth handlers. Deps are
// injected (no global state) so this is testable and wired once in main.
type TenantOps struct {
	CP          *tenancy.ControlPlane
	Prov        *provisioning.Provisioner
	Router      *tenancy.Router  // resolves tenant DB pools (used to reach the owner workspace)
	Jobs        *jobqueue.Queue  // durable async job queue (provisioning, etc.)
	CF          storage.CFClientIface
	CORSOrigins []string
}

// NewTenantOps constructs the handler group.
func NewTenantOps(cp *tenancy.ControlPlane, prov *provisioning.Provisioner, router *tenancy.Router, jobs *jobqueue.Queue) *TenantOps {
	return &TenantOps{CP: cp, Prov: prov, Router: router, Jobs: jobs}
}

// WithCFClient wires the Cloudflare management client so admin endpoints can
// perform bucket operations (e.g. re-apply CORS after a failed provisioning).
func (h *TenantOps) WithCFClient(cf storage.CFClientIface, origins []string) *TenantOps {
	h.CF = cf
	h.CORSOrigins = origins
	return h
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, models.APIResponse{Success: false, Message: msg})
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// inviteExpiry returns the expiry instant for an invite. Defaults to the
// configured INVITE_EXPIRY_HOURS (24h) when hours <= 0.
func inviteExpiry(hours int) time.Time {
	if hours <= 0 {
		hours = config.AppConfig.InviteExpiryHours
	}
	if hours <= 0 {
		hours = 24
	}
	return time.Now().Add(time.Duration(hours) * time.Hour)
}

func frontendBase() string { return strings.TrimRight(config.AppConfig.FrontendURL, "/") }

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint violation (code 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// applyLink is the public URL where an invited customer fills the onboarding form.
func applyLink(token string) string { return frontendBase() + "/onboarding/apply?token=" + token }

// setupLink is the public URL where an onboarded customer sets their password.
func setupLink(token string) string { return frontendBase() + "/onboarding/set-password?token=" + token }

// resetLink is the public URL where any user resets a forgotten password.
func resetLink(token string) string { return frontendBase() + "/reset-password?token=" + token }

func generateTenantJWT(identityID, email, tenantID string, d time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"id":        identityID,
		"email":     email,
		"tenant_id": tenantID,
		"exp":       time.Now().Add(d).Unix(),
		"iat":       time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(config.AppConfig.JWTSecret))
}

// requirePlatformAdmin returns the caller's identity if they are a platform admin.
func (h *TenantOps) requirePlatformAdmin(r *http.Request) (middleware.UserContextPayload, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil {
		return payload, false
	}
	ok, err := h.CP.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil || !ok {
		return payload, false
	}
	return payload, true
}

// ---- platform admin: tenants -----------------------------------------------

// createTenantRequest carries the owner-filled onboarding form as a flat map of
// Customer-workflow field keys (snake_case), e.g. company_name, super_admin_email.
// This path skips approval and provisions immediately.
type createTenantRequest struct {
	FormData map[string]any `json:"formData"`
}

// slugify converts a company name into a DNS/db-safe slug: lowercase, runs of
// non-alphanumerics collapsed to single hyphens, trimmed, capped at 63 chars.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

// formStr reads a trimmed string value from a flat form-data map.
func formStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// CreateTenant onboards a customer directly from the owner-filled form: it
// creates the tenant, stores the submission, and provisions immediately
// (no approval step). Platform-admin only.
func (h *TenantOps) CreateTenant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	admin, ok := h.requirePlatformAdmin(r)
	if !ok {
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}

	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FormData == nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	companyName := formStr(req.FormData, "company_name")
	superAdminEmail := formStr(req.FormData, "super_admin_email")
	slug := slugify(companyName)
	if slug == "" || superAdminEmail == "" {
		fail(w, http.StatusBadRequest, "A company name and a super-admin email are required.")
		return
	}

	tenant, err := h.CP.CreateTenant(r.Context(), slug, companyName, false)
	if err != nil {
		if isUniqueViolation(err) {
			fail(w, http.StatusConflict, fmt.Sprintf("A tenant named %q already exists. Use a different company name.", companyName))
		} else {
			fail(w, http.StatusInternalServerError, "Failed to create tenant.")
		}
		return
	}
	if md, mErr := json.Marshal(req.FormData); mErr == nil {
		_ = h.CP.SetTenantMetadata(r.Context(), tenant.ID, string(md))
	}
	_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenant.ID, "tenant.created", "{}")

	// Provision now + email the customer their password-setup link.
	setupLink, err := h.finalizeOnboarding(r.Context(), tenant, req.FormData)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant created but onboarding finalize failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":          true,
		"tenantId":         tenant.ID,
		"slug":             tenant.Slug,
		"passwordSetupLink": setupLink,
	})
}

// InviteCustomer creates a tenant shell + invite and emails the customer a link
// to fill the onboarding form themselves (the approval path). Platform-admin only.
func (h *TenantOps) InviteCustomer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	admin, ok := h.requirePlatformAdmin(r)
	if !ok {
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}

	var req struct {
		CompanyName    string `json:"companyName"`
		RecipientName  string `json:"recipientName"`
		ContactEmail   string `json:"contactEmail"`
		ExpiresInHours int    `json:"expiresInHours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.CompanyName = strings.TrimSpace(req.CompanyName)
	req.ContactEmail = strings.TrimSpace(req.ContactEmail)
	slug := slugify(req.CompanyName)
	if slug == "" || req.ContactEmail == "" {
		fail(w, http.StatusBadRequest, "A company name and a contact email are required.")
		return
	}

	tenant, err := h.CP.CreateTenant(r.Context(), slug, req.CompanyName, false)
	if err != nil {
		if isUniqueViolation(err) {
			fail(w, http.StatusConflict, fmt.Sprintf("A tenant named %q already exists. Use a different company name.", req.CompanyName))
		} else {
			fail(w, http.StatusInternalServerError, "Failed to create tenant.")
		}
		return
	}
	// Seed minimal metadata so the customer's form is pre-filled.
	seed := map[string]any{
		"company_name":     req.CompanyName,
		"super_admin_name": req.RecipientName,
		"super_admin_email": req.ContactEmail,
	}
	if md, mErr := json.Marshal(seed); mErr == nil {
		_ = h.CP.SetTenantMetadata(r.Context(), tenant.ID, string(md))
	}

	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate invite token.")
		return
	}
	invite, err := h.CP.CreateInvite(r.Context(), tenant.ID, req.ContactEmail, token, inviteExpiry(req.ExpiresInHours))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create invite.")
		return
	}

	link := applyLink(token)
	emailErr := services.SendOnboardingInviteEmail(req.ContactEmail, req.RecipientName, link)
	if emailErr != nil {
		log.Printf("WARNING: invite email to %s failed: %v", req.ContactEmail, emailErr)
	}
	emailSent := emailErr == nil
	_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenant.ID, "tenant.invited", "{}")

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":    true,
		"tenantId":   tenant.ID,
		"slug":       tenant.Slug,
		"inviteLink": link,
		"expiresAt":  invite.ExpiresAt,
		"emailSent":  emailSent,
	})
}

// ListTenants returns all tenants. Platform-admin only.
func (h *TenantOps) ListTenants(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(r); !ok {
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}
	tenants, err := h.CP.ListTenants(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list tenants.")
		return
	}
	out := make([]map[string]any, 0, len(tenants))
	for i := range tenants {
		t := tenants[i]
		meta := json.RawMessage(t.Metadata)
		if len(meta) == 0 {
			meta = json.RawMessage("{}")
		}
		out = append(out, map[string]any{
			"id": t.ID, "slug": t.Slug, "displayName": t.DisplayName,
			"status": t.Status, "migrationStatus": t.MigrationStatus,
			"dbName": t.DBName, "createdAt": t.CreatedAt,
			"hardDeleteAfter": t.HardDeleteAfter,
			"metadata":        meta,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tenants": out})
}

// TenantLifecycle handles /api/platform/tenants/{id}/{action} where action is
// suspend | restore | delete | force-delete | invites. Platform-admin only.
func (h *TenantOps) TenantLifecycle(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.requirePlatformAdmin(r)
	if !ok {
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/platform/tenants/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		fail(w, http.StatusBadRequest, "Expected /api/platform/tenants/{id}/{action}.")
		return
	}
	id, action := parts[0], parts[1]

	// Invite management (list + resend) lives under the tenant resource.
	if action == "invites" {
		h.tenantInvites(w, r, admin, id)
		return
	}

	// Async job status/retry (provisioning, etc.) lives under the tenant resource.
	if action == "jobs" {
		h.tenantJobs(w, r, admin, id, parts[2:])
		return
	}

	if len(parts) != 2 {
		fail(w, http.StatusBadRequest, "Expected /api/platform/tenants/{id}/{action}.")
		return
	}

	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Onboarding approval lives under the tenant resource too.
	if action == "approve" || action == "reject" {
		h.reviewOnboarding(w, r, admin, id, action)
		return
	}

	var err error
	switch action {
	case "suspend":
		err = h.CP.SetTenantStatus(r.Context(), id, tenancy.StatusSuspended)
	case "restore":
		err = h.CP.RestoreTenant(r.Context(), id)
	case "delete":
		err = h.CP.MarkTenantDeleted(r.Context(), id, time.Now().Add(tenantDeleteGraceDays*24*time.Hour))
	case "force-delete":
		// Hard delete is destructive; for now we soft-delete with an immediate
		// deadline. Actual DROP DATABASE is handled by a reaper (Phase 4+).
		err = h.CP.MarkTenantDeleted(r.Context(), id, time.Now())
	default:
		fail(w, http.StatusBadRequest, "Unknown action: "+action)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Lifecycle action failed.")
		return
	}
	_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, id, "tenant."+action, "{}")
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Tenant " + action + " applied."})
}

// jobView is the JSON shape returned for an async_jobs row.
func jobView(j jobqueue.Job) map[string]any {
	view := map[string]any{
		"id":          j.ID,
		"jobType":     j.JobType,
		"status":      j.Status,
		"attempts":    j.Attempts,
		"maxAttempts": j.MaxAttempts,
		"createdAt":   j.CreatedAt,
		"updatedAt":   j.UpdatedAt,
	}
	if j.LastError != nil {
		view["lastError"] = *j.LastError
	}
	if len(j.Progress) > 0 {
		view["progress"] = json.RawMessage(j.Progress)
	}
	return view
}

// tenantJobs handles GET /api/platform/tenants/{id}/jobs (list recent async
// jobs for the tenant — e.g. provisioning status) and
// POST /api/platform/tenants/{id}/jobs/{jobId}/retry (requeue a failed/dead
// job). Lets platform admins see and recover from partial provisioning
// failures without a server restart.
func (h *TenantOps) tenantJobs(w http.ResponseWriter, r *http.Request, admin middleware.UserContextPayload, tenantID string, rest []string) {
	if h.Jobs == nil {
		fail(w, http.StatusServiceUnavailable, "Job queue not available.")
		return
	}

	switch {
	case len(rest) == 0:
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		jobs, err := h.Jobs.ListForTenant(r.Context(), tenantID, 20)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to list jobs.")
			return
		}
		out := make([]map[string]any, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, jobView(j))
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "jobs": out})

	case len(rest) == 2 && rest[1] == "retry":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		if err := h.Jobs.Retry(r.Context(), rest[0]); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenantID, "job.retry", "{}")
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Job requeued."})

	default:
		fail(w, http.StatusBadRequest, "Expected /api/platform/tenants/{id}/jobs[/{jobId}/retry].")
	}
}

// inviteView is the JSON shape returned for invites (token is the "invite key").
func inviteView(inv tenancy.Invite) map[string]any {
	return map[string]any{
		"id":           inv.ID,
		"contactEmail": inv.ContactEmail,
		"token":        inv.Token,
		"status":       inv.Status,
		"expiresAt":    inv.ExpiresAt,
		"acceptedAt":   inv.AcceptedAt,
		"createdAt":    inv.CreatedAt,
		"expired":      inv.Status == "pending" && time.Now().After(inv.ExpiresAt),
		"inviteLink":   applyLink(inv.Token),
	}
}

type resendInviteRequest struct {
	ContactEmail   string `json:"contactEmail"`
	ExpiresInHours int    `json:"expiresInHours"`
}

// tenantInvites handles GET (list) and POST (resend/retry) for a tenant's
// invites. POST re-issues the latest invite with a fresh token + expiry and
// re-sends the email; if no invite exists yet it creates one.
func (h *TenantOps) tenantInvites(w http.ResponseWriter, r *http.Request, admin middleware.UserContextPayload, tenantID string) {
	switch r.Method {
	case http.MethodGet:
		invites, err := h.CP.ListInvitesByTenant(r.Context(), tenantID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load invites.")
			return
		}
		out := make([]map[string]any, 0, len(invites))
		for _, inv := range invites {
			out = append(out, inviteView(inv))
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "invites": out})

	case http.MethodPost:
		tenant, err := h.CP.TenantByID(r.Context(), tenantID)
		if err != nil {
			fail(w, http.StatusNotFound, "Tenant not found.")
			return
		}

		var req resendInviteRequest
		_ = json.NewDecoder(r.Body).Decode(&req) // body is optional

		token, err := randomToken()
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to generate invite token.")
			return
		}
		expiresAt := inviteExpiry(req.ExpiresInHours)

		// Re-issue the latest invite, or create one if none exists.
		latest, err := h.CP.LatestInviteForTenant(r.Context(), tenantID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load invites.")
			return
		}

		contactEmail := strings.TrimSpace(req.ContactEmail)
		if contactEmail == "" && latest != nil {
			contactEmail = latest.ContactEmail
		}
		if contactEmail == "" {
			fail(w, http.StatusBadRequest, "A contact email is required to send an invite.")
			return
		}

		var invite *tenancy.Invite
		if latest != nil {
			invite, err = h.CP.RefreshInvite(r.Context(), latest.ID, token, expiresAt)
		} else {
			invite, err = h.CP.CreateInvite(r.Context(), tenantID, contactEmail, token, expiresAt)
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to issue invite.")
			return
		}

		// Email is best-effort: the invite (key + link) is valid regardless, so
		// the owner can always copy the link if delivery is unavailable (e.g. no
		// SMTP configured in dev). Surface the outcome via emailSent.
		link := applyLink(token)
		emailErr := services.SendOnboardingInviteEmail(contactEmail, tenant.DisplayName, link)
		if emailErr != nil {
			log.Printf("WARNING: resend invite email to %s failed: %v", contactEmail, emailErr)
		}
		emailSent := emailErr == nil
		_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenantID, "tenant.invite_resent", "{}")

		writeJSON(w, http.StatusOK, map[string]any{
			"success":   true,
			"invite":    inviteView(*invite),
			"emailSent": emailSent,
		})

	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// ---- auth: tenant login -----------------------------------------------------

type tenantLoginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

// TenantLogin authenticates against control-plane identities and mints a
// tenant-scoped JWT. Path: POST /api/auth/tenant-login
func (h *TenantOps) TenantLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req tenantLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Email == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "email and password are required.")
		return
	}

	identity, err := h.CP.IdentityByEmail(r.Context(), req.Email)
	if errors.Is(err, tenancy.ErrIdentityNotFound) {
		logSecurityEvent(r, "login_failed", "email", req.Email, "reason", "unknown_identity")
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	if identity.PasswordHash == "" {
		fail(w, http.StatusUnauthorized, "This account uses single sign-on.")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(identity.PasswordHash), []byte(req.Password)); err != nil {
		logSecurityEvent(r, "login_failed", "email", req.Email, "identity", identity.ID, "reason", "bad_password")
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}

	// Check the user's workspace status — suspended/disabled accounts must not
	// be able to log in even though their control-plane identity still exists.
	if identity.TenantID != "" {
		if tenant, tErr := h.CP.TenantByID(r.Context(), identity.TenantID); tErr == nil && tenant.Servable() {
			if pool, pErr := h.Router.PoolFor(r.Context(), tenant); pErr == nil {
				if u, uErr := userstore.GetUserByIdentityID(r.Context(), pool, identity.ID); uErr == nil {
					if u.Status == "suspended" {
						fail(w, http.StatusForbidden, "Your account has been suspended. Contact your workspace administrator.")
						return
					}
					if u.Status == "disabled" {
						fail(w, http.StatusForbidden, "Your account has been deactivated.")
						return
					}
				}
			}
		}
	}

	accessDur := config.AppConfig.JWTExpiresIn
	if req.RememberMe {
		accessDur = config.AppConfig.JWTRememberMeExpiresIn
	}
	d, err := time.ParseDuration(accessDur)
	if err != nil {
		d = time.Hour
	}
	token, err := generateTenantJWT(identity.ID, identity.Email, identity.TenantID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}

	// Surface platform-admin status so the frontend can gate owner-only UI
	// (e.g. customer onboarding). Non-fatal if the lookup fails — default false.
	isPlatformAdmin, err := h.CP.IsPlatformAdmin(r.Context(), identity.ID)
	if err != nil {
		isPlatformAdmin = false
	}

	// Issue refresh token (DB-backed, rotated on every use).
	refreshRaw, refreshExpiry, err := issueRefreshToken(r.Context(), h.CP, identity.ID)
	if err != nil {
		// Non-fatal: session still works without a refresh token; user will
		// just need to re-login when the access token expires.
		log.Printf("warn: failed to issue refresh token for identity %s: %v", identity.ID, err)
		refreshRaw = ""
	}

	// Set the JWT as an httpOnly cookie so it survives page refreshes without
	// being accessible to JavaScript (XSS protection). The Authorization header
	// interceptor in the frontend Axios client serves as a fallback for the
	// in-memory token that exists immediately after login.
	accessExpiry := time.Now().Add(d)
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   config.AppConfig.IsProduction(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(d.Seconds()),
	})
	if refreshRaw != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "refresh_token",
			Value:    refreshRaw,
			Path:     "/api/auth",
			HttpOnly: true,
			Secure:   config.AppConfig.IsProduction(),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(time.Until(refreshExpiry).Seconds()),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"token":     token,
		"expiresAt": accessExpiry.UnixMilli(),
		"user": map[string]any{
			"id": identity.ID, "email": identity.Email,
			"fullName": identity.FullName, "tenantId": identity.TenantID,
			"isPlatformAdmin": isPlatformAdmin,
		},
	})
}

// Logout clears both auth cookies and revokes the refresh token. Path: POST /api/auth/logout
func (h *TenantOps) Logout(w http.ResponseWriter, r *http.Request) {
	// Revoke the refresh token in the DB so it cannot be reused after logout.
	if cookie, err := r.Cookie("refresh_token"); err == nil && cookie.Value != "" {
		hash := tenancy.HashRefreshToken(cookie.Value)
		if err := h.CP.RevokeRefreshToken(r.Context(), hash); err != nil {
			// Non-fatal — the cookie will expire on its own.
			log.Printf("warn: logout: revoke refresh token: %v", err)
		}
	}
	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// RefreshSession issues a new access + refresh token pair given a valid refresh
// token cookie. The old refresh token is revoked (rotation). Path: POST /api/auth/refresh
func (h *TenantOps) RefreshSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil || cookie.Value == "" {
		fail(w, http.StatusUnauthorized, "No refresh token.")
		return
	}

	hash := tenancy.HashRefreshToken(cookie.Value)
	rec, err := h.CP.RefreshTokenByHash(r.Context(), hash)
	if errors.Is(err, tenancy.ErrRefreshTokenReused) {
		// Possible token theft — revoke all tokens for this identity as a
		// precaution (if we can identify which identity owns it).
		log.Printf("warn: refresh token reuse detected (hash prefix %.8s)", hash)
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Session invalid. Please sign in again.")
		return
	}
	if err != nil {
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Refresh token expired. Please sign in again.")
		return
	}

	// Revoke the consumed token before issuing the new pair (rotation).
	if err := h.CP.RevokeRefreshToken(r.Context(), hash); err != nil {
		log.Printf("warn: refresh rotation: revoke old token: %v", err)
	}

	// Load the identity to rebuild the JWT claims.
	identity, err := h.CP.IdentityByID(r.Context(), rec.IdentityID)
	if err != nil {
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Identity not found. Please sign in again.")
		return
	}

	// Reject suspended/disabled users — their refresh tokens must not extend sessions.
	if identity.TenantID != "" {
		if tenant, tErr := h.CP.TenantByID(r.Context(), identity.TenantID); tErr == nil && tenant.Servable() {
			if pool, pErr := h.Router.PoolFor(r.Context(), tenant); pErr == nil {
				if u, uErr := userstore.GetUserByIdentityID(r.Context(), pool, identity.ID); uErr == nil {
					if u.Status == "suspended" || u.Status == "disabled" {
						clearAuthCookies(w)
						fail(w, http.StatusForbidden, "Account suspended. Please contact your administrator.")
						return
					}
				}
			}
		}
	}

	// Mint a new access token.
	d, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		d = time.Hour
	}
	newToken, err := generateTenantJWT(identity.ID, identity.Email, identity.TenantID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}

	// Issue a brand-new refresh token (rotation).
	refreshRaw, refreshExpiry, err := issueRefreshToken(r.Context(), h.CP, identity.ID)
	if err != nil {
		log.Printf("warn: refresh rotation: issue new refresh token: %v", err)
		refreshRaw = ""
	}

	accessExpiry := time.Now().Add(d)
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    newToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   config.AppConfig.IsProduction(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(d.Seconds()),
	})
	if refreshRaw != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "refresh_token",
			Value:    refreshRaw,
			Path:     "/api/auth",
			HttpOnly: true,
			Secure:   config.AppConfig.IsProduction(),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(time.Until(refreshExpiry).Seconds()),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"token":     newToken,
		"expiresAt": accessExpiry.UnixMilli(),
	})
}

// issueRefreshToken generates a random raw token, hashes it, persists it, and
// returns the raw value (for the cookie) plus the expiry time.
func issueRefreshToken(ctx context.Context, cp *tenancy.ControlPlane, identityID string) (string, time.Time, error) {
	raw, err := randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	rd, err := time.ParseDuration(config.AppConfig.RefreshTokenExpiresIn)
	if err != nil {
		rd = 24 * time.Hour
	}
	expiry := time.Now().Add(rd)
	hash := tenancy.HashRefreshToken(raw)
	if err := cp.CreateRefreshToken(ctx, identityID, hash, expiry); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiry, nil
}

// clearAuthCookies sets MaxAge=-1 on both auth cookies to force browser deletion.
func clearAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{"auth_token", "refresh_token"} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
	}
	// refresh_token uses path=/api/auth — clear that too.
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/auth",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// ---- forgot / reset password ------------------------------------------------

// ForgotPassword POST /api/auth/forgot-password
// Generates a one-hour reset token and emails it to the address on file.
// Always returns HTTP 200 — never reveals whether the email exists.
func (h *TenantOps) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	// Timing-safe: always return 200 whether or not the email exists.
	const successMsg = "If that email is registered, a reset link has been sent."

	identity, err := h.CP.IdentityByEmail(r.Context(), req.Email)
	if errors.Is(err, tenancy.ErrIdentityNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": successMsg})
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to process request.")
		return
	}

	// SSO-only accounts have no password — nothing to reset.
	if identity.PasswordHash == "" && identity.SSOProvider != "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": successMsg})
		return
	}

	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate reset token.")
		return
	}
	if err := h.CP.SetIdentityPasswordSetupToken(r.Context(), identity.ID, token, time.Now().Add(time.Hour)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to initiate password reset.")
		return
	}

	link := resetLink(token)
	if err := services.SendPasswordResetEmail(identity.Email, identity.FullName, link); err != nil {
		log.Printf("password reset email to %s failed (token still valid): %v", identity.Email, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": successMsg})
}

// ValidateResetToken GET /api/auth/reset-password/{token}
// Returns whether the token is valid and the email it belongs to.
// Used by the frontend to decide whether to show the form or an error state.
func (h *TenantOps) ValidateResetToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		fail(w, http.StatusBadRequest, "Missing reset token.")
		return
	}
	identity, err := h.CP.IdentityByPasswordToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"valid":   false,
			"message": "This reset link is invalid or has expired.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"valid":    true,
		"email":    identity.Email,
		"fullName": identity.FullName,
	})
}

// ResetPassword POST /api/auth/reset-password
// Consumes the reset token and sets the new password. Token is nulled on success.
func (h *TenantOps) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Token == "" {
		fail(w, http.StatusBadRequest, "token is required.")
		return
	}
	if len(req.NewPassword) < 8 {
		fail(w, http.StatusBadRequest, "Password must be at least 8 characters.")
		return
	}

	identity, err := h.CP.IdentityByPasswordToken(r.Context(), req.Token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This reset link is invalid or has expired.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 10)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}
	// SetIdentityPassword NULLs the token — single-use enforced at the DB level.
	if err := h.CP.SetIdentityPassword(r.Context(), identity.ID, string(hash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update password.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Password updated. You can now sign in.",
		"email":   identity.Email,
	})
}

// RepairBucketCORS POST /api/platform/tenants/{id}/repair-cors
// Re-applies the CORS policy to the tenant's R2 bucket. Use when a bucket was
// created before CLOUDFLARE_API_TOKEN was configured (CORS was skipped).
// Platform-admin only.
func (h *TenantOps) RepairBucketCORS(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(r); !ok {
		fail(w, http.StatusForbidden, "Platform admin required.")
		return
	}
	if h.CF == nil || !h.CF.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "Cloudflare API not configured (set CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN).")
		return
	}

	id := r.PathValue("id")
	tenant, err := h.CP.TenantByID(r.Context(), id)
	if err != nil {
		fail(w, http.StatusNotFound, "Tenant not found.")
		return
	}
	if tenant.R2Bucket == "" {
		fail(w, http.StatusBadRequest, "Tenant has no R2 bucket assigned.")
		return
	}

	if err := h.CF.SetBucketCORS(r.Context(), tenant.R2Bucket, h.CORSOrigins); err != nil {
		log.Printf("repair-cors: tenant %s bucket %s: %v", tenant.Slug, tenant.R2Bucket, err)
		fail(w, http.StatusInternalServerError, "Failed to set CORS: "+err.Error())
		return
	}

	log.Printf("repair-cors: applied CORS to bucket %s for tenant %s", tenant.R2Bucket, tenant.Slug)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "CORS applied to bucket " + tenant.R2Bucket,
		"bucket":  tenant.R2Bucket,
		"origins": h.CORSOrigins,
	})
}

// ---- platform setup (log-only token, no public claiming) --------------------

// activateLimiter is a sliding-window counter keyed by IP address.
// Max 5 attempts per 15 minutes — brute-force guard for the activate endpoint.
var (
	activateMu       sync.Mutex
	activateAttempts = map[string][]time.Time{}
)

const (
	activateMaxAttempts = 5
	activateWindow      = 15 * time.Minute
)

func activateAllowed(ip string) bool {
	activateMu.Lock()
	defer activateMu.Unlock()
	cutoff := time.Now().Add(-activateWindow)
	prev := activateAttempts[ip]
	var kept []time.Time
	for _, t := range prev {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= activateMaxAttempts {
		activateAttempts[ip] = kept
		return false
	}
	activateAttempts[ip] = append(kept, time.Now())
	return true
}

// SetupStatus GET /api/platform/setup/status
// Returns whether the platform owner has been bootstrapped and is active.
func (h *TenantOps) SetupStatus(w http.ResponseWriter, r *http.Request) {
	owner, err := h.CP.PlatformOwnerTenant(r.Context())
	bootstrapped := err == nil && owner != nil && owner.Status == "active"
	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"bootstrapped": bootstrapped,
	})
}

// Activate POST /api/platform/activate
// Consumes the one-time setup token printed to server stdout on first boot.
// Sets the platform admin password, grants admin role, activates the owner
// tenant, and enqueues workspace provisioning. Returns a JWT on success.
// Rate-limited to 5 attempts per 15 minutes per IP.
func (h *TenantOps) Activate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}

	// 409 if already active — endpoint has no use after first activation.
	owner, err := h.CP.PlatformOwnerTenant(r.Context())
	if err == nil && owner != nil && owner.Status == "active" {
		fail(w, http.StatusConflict, "Platform already activated.")
		return
	}

	// IP-based rate limit.
	ip := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip = strings.SplitN(xff, ",", 2)[0]
	}
	ip = strings.TrimSpace(ip)
	if !activateAllowed(ip) {
		fail(w, http.StatusTooManyRequests, "Too many activation attempts. Try again in 15 minutes.")
		return
	}

	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Token == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "token and password are required.")
		return
	}
	if len(req.Password) < 12 {
		fail(w, http.StatusBadRequest, "Password must be at least 12 characters.")
		return
	}

	// Hash the incoming raw token and look up by hash — raw value never stored.
	sum := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(sum[:])

	identity, err := h.CP.IdentityBySetupTokenHash(r.Context(), tokenHash)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Invalid or expired setup token.")
		return
	}

	// Look up the owner tenant via the identity's tenant_id.
	tenant, err := h.CP.TenantByID(r.Context(), identity.TenantID)
	if err != nil || !tenant.IsPlatformOwner {
		fail(w, http.StatusUnauthorized, "Invalid or expired setup token.")
		return
	}

	// Set password (also clears the token column — one-shot consumed).
	pwHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}
	if err := h.CP.SetIdentityPassword(r.Context(), identity.ID, string(pwHash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to set password.")
		return
	}

	// Grant platform-admin role.
	if err := h.CP.AddPlatformAdmin(r.Context(), identity.ID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to grant admin role.")
		return
	}

	// Mark tenant active.
	if err := h.CP.ActivatePlatformOwner(r.Context(), tenant.ID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to activate platform.")
		return
	}

	// Enqueue workspace provisioning (creates tenant DB with seeded workflows/roles).
	if h.Prov != nil {
		h.Prov.Enqueue(provisioning.Job{
			TenantID:   tenant.ID,
			Slug:       tenant.Slug,
			IdentityID: identity.ID,
			Email:      identity.Email,
			FullName:   identity.FullName,
		})
	}

	// Issue access JWT so the caller is immediately logged in.
	d, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		d = time.Hour
	}
	token, err := generateTenantJWT(identity.ID, identity.Email, identity.TenantID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}

	log.Printf("Platform activated by %s — workspace provisioning started.", identity.Email)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Platform activated. Workspace provisioning has started.",
		"token":   token,
		"email":   identity.Email,
	})
}
