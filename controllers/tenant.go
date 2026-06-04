package controllers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// inviteGraceDefault is how long a soft-deleted tenant survives before hard delete.
const tenantDeleteGraceDays = 30

// TenantOps groups the multi-tenant platform/onboarding/auth handlers. Deps are
// injected (no global state) so this is testable and wired once in main.
type TenantOps struct {
	CP     *tenancy.ControlPlane
	Prov   *provisioning.Provisioner
	Router *tenancy.Router // resolves tenant DB pools (used to reach the owner workspace)
}

// NewTenantOps constructs the handler group.
func NewTenantOps(cp *tenancy.ControlPlane, prov *provisioning.Provisioner, router *tenancy.Router) *TenantOps {
	return &TenantOps{CP: cp, Prov: prov, Router: router}
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

// applyLink is the public URL where an invited customer fills the onboarding form.
func applyLink(token string) string { return frontendBase() + "/onboarding/apply?token=" + token }

// setupLink is the public URL where an onboarded customer sets their password.
func setupLink(token string) string { return frontendBase() + "/onboarding/set-password?token=" + token }

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
		fail(w, http.StatusInternalServerError, "Failed to create tenant (slug may be taken).")
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
		fail(w, http.StatusInternalServerError, "Failed to create tenant (slug may be taken).")
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
	emailSent := services.SendOnboardingInviteEmail(req.ContactEmail, req.RecipientName, link) == nil
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
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		fail(w, http.StatusBadRequest, "Expected /api/platform/tenants/{id}/{action}.")
		return
	}
	id, action := parts[0], parts[1]

	// Invite management (list + resend) lives under the tenant resource.
	if action == "invites" {
		h.tenantInvites(w, r, admin, id)
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
		emailSent := services.SendOnboardingInviteEmail(contactEmail, tenant.DisplayName, link) == nil
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
		fail(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}

	dur := config.AppConfig.JWTExpiresIn
	if req.RememberMe {
		dur = config.AppConfig.JWTRememberMeExpiresIn
	}
	d, err := time.ParseDuration(dur)
	if err != nil {
		d = 24 * time.Hour
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

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"token":   token,
		"user": map[string]any{
			"id": identity.ID, "email": identity.Email,
			"fullName": identity.FullName, "tenantId": identity.TenantID,
			"isPlatformAdmin": isPlatformAdmin,
		},
	})
}
