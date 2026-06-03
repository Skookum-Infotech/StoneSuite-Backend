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
	CP   *tenancy.ControlPlane
	Prov *provisioning.Provisioner
}

// NewTenantOps constructs the handler group.
func NewTenantOps(cp *tenancy.ControlPlane, prov *provisioning.Provisioner) *TenantOps {
	return &TenantOps{CP: cp, Prov: prov}
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

// inviteExpiry returns the expiry instant for an invite. Defaults to 72h
// (matching the legacy onboarding default) when hours <= 0.
func inviteExpiry(hours int) time.Time {
	if hours <= 0 {
		hours = 72
	}
	return time.Now().Add(time.Duration(hours) * time.Hour)
}

// inviteLink builds the public acceptance URL for an invite token.
func inviteLink(token string) string {
	return strings.TrimRight(config.AppConfig.FrontendURL, "/") + "/onboarding/accept?token=" + token
}

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

// createTenantRequest carries the rich customer-onboarding form. Only a few
// fields drive provisioning (slug/displayName/contactEmail); everything else is
// captured as tenant metadata. slug is optional — derived from companyName when
// omitted. displayName falls back to companyName, contactEmail to superAdminEmail.
type createTenantRequest struct {
	Slug         string `json:"slug"`
	DisplayName  string `json:"displayName"`
	ContactEmail string `json:"contactEmail"`

	// Invite expiry in hours (default 72). Mirrors dev's configurable expiry.
	ExpiresInHours int `json:"expiresInHours"`

	// Rich company profile (stored as metadata).
	CompanyName  string `json:"companyName"`
	LegalName    string `json:"legalName"`
	Industry     string `json:"industry"`
	Website      string `json:"website"`
	Country      string `json:"country"`
	Currency     string `json:"currency"`
	Timezone     string `json:"timezone"`
	TaxID        string `json:"taxId"`

	BillingAddress  string `json:"billingAddress"`
	ShippingAddress string `json:"shippingAddress"`
	ReturnAddress   string `json:"returnAddress"`

	SuperAdminName     string `json:"superAdminName"`
	SuperAdminEmail    string `json:"superAdminEmail"`
	SuperAdminPhone    string `json:"superAdminPhone"`
	SuperAdminJobTitle string `json:"superAdminJobTitle"`

	FinanceName  string `json:"financeName"`
	FinanceEmail string `json:"financeEmail"`
	FinancePhone string `json:"financePhone"`
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

// CreateTenant creates a tenant and sends an onboarding invite. Platform-admin only.
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	// Derive the provisioning essentials from the rich form when not given.
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(req.CompanyName)
	}
	contactEmail := strings.TrimSpace(req.ContactEmail)
	if contactEmail == "" {
		contactEmail = strings.TrimSpace(req.SuperAdminEmail)
	}
	slug := slugify(req.Slug)
	if slug == "" {
		slug = slugify(displayName)
	}
	if slug == "" || displayName == "" || contactEmail == "" {
		fail(w, http.StatusBadRequest, "A company name and a super-admin (contact) email are required.")
		return
	}

	tenant, err := h.CP.CreateTenant(r.Context(), slug, displayName, false)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create tenant (slug may be taken).")
		return
	}

	// Persist the rich company profile as tenant metadata (best-effort).
	metadata := map[string]any{
		"companyName": req.CompanyName, "legalName": req.LegalName,
		"industry": req.Industry, "website": req.Website,
		"country": req.Country, "currency": req.Currency,
		"timezone": req.Timezone, "taxId": req.TaxID,
		"billingAddress": req.BillingAddress, "shippingAddress": req.ShippingAddress,
		"returnAddress": req.ReturnAddress,
		"superAdmin": map[string]string{
			"name": req.SuperAdminName, "email": req.SuperAdminEmail,
			"phone": req.SuperAdminPhone, "jobTitle": req.SuperAdminJobTitle,
		},
		"finance": map[string]string{
			"name": req.FinanceName, "email": req.FinanceEmail, "phone": req.FinancePhone,
		},
	}
	if md, mErr := json.Marshal(metadata); mErr == nil {
		_ = h.CP.SetTenantMetadata(r.Context(), tenant.ID, string(md))
	}

	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate invite token.")
		return
	}
	invite, err := h.CP.CreateInvite(r.Context(), tenant.ID, contactEmail, token, inviteExpiry(req.ExpiresInHours))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create invite.")
		return
	}

	link := inviteLink(token)
	if err := services.SendOnboardingInviteEmail(contactEmail, displayName, link); err != nil {
		// Non-fatal: invite exists; link can be re-sent.
		_ = err
	}
	_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenant.ID, "tenant.created", "{}")

	writeJSON(w, http.StatusCreated, map[string]any{
		"success":    true,
		"tenantId":   tenant.ID,
		"slug":       tenant.Slug,
		"inviteLink": link,
		"expiresAt":  invite.ExpiresAt,
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
		out = append(out, map[string]any{
			"id": t.ID, "slug": t.Slug, "displayName": t.DisplayName,
			"status": t.Status, "migrationStatus": t.MigrationStatus,
			"dbName": t.DBName, "createdAt": t.CreatedAt,
			"hardDeleteAfter": t.HardDeleteAfter,
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
		"inviteLink":   inviteLink(inv.Token),
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
		link := inviteLink(token)
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

// ---- onboarding: accept invite ---------------------------------------------

// GetInvite returns invite details for the accept screen.
// Path: /api/onboarding/tenant-invite/{token}
func (h *TenantOps) GetInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/api/onboarding/tenant-invite/")
	if token == "" {
		fail(w, http.StatusBadRequest, "Missing invite token.")
		return
	}
	inv, err := h.CP.InviteByToken(r.Context(), token)
	if errors.Is(err, tenancy.ErrInviteNotFound) {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invite.")
		return
	}
	tenant, err := h.CP.TenantByID(r.Context(), inv.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load tenant.")
		return
	}
	valid := inv.Status == "pending" && time.Now().Before(inv.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "valid": valid, "status": inv.Status,
		"contactEmail": inv.ContactEmail, "tenantName": tenant.DisplayName,
	})
}

type acceptInviteRequest struct {
	Token    string `json:"token"`
	FullName string `json:"fullName"`
	Password string `json:"password"`
}

// AcceptInvite creates the accepting identity and enqueues provisioning.
// Path: POST /api/onboarding/tenant-accept
func (h *TenantOps) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req acceptInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Token == "" || req.FullName == "" || len(req.Password) < 8 {
		fail(w, http.StatusBadRequest, "token, fullName and a password (min 8 chars) are required.")
		return
	}

	inv, err := h.CP.InviteByToken(r.Context(), req.Token)
	if errors.Is(err, tenancy.ErrInviteNotFound) {
		fail(w, http.StatusNotFound, "Invite not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invite.")
		return
	}
	if inv.Status != "pending" || time.Now().After(inv.ExpiresAt) {
		fail(w, http.StatusBadRequest, "This invite is no longer valid.")
		return
	}

	tenant, err := h.CP.TenantByID(r.Context(), inv.TenantID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load tenant.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}
	identity, err := h.CP.CreateIdentity(r.Context(), inv.TenantID, inv.ContactEmail, string(hash), req.FullName, true)
	if err != nil {
		fail(w, http.StatusConflict, "An account for this email may already exist.")
		return
	}
	if err := h.CP.MarkInviteAccepted(r.Context(), inv.ID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update invite.")
		return
	}

	h.Prov.Enqueue(provisioning.Job{
		TenantID:   tenant.ID,
		Slug:       tenant.Slug,
		IdentityID: identity.ID,
		Email:      identity.Email,
		FullName:   identity.FullName,
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"message": "Account created. Your workspace is being set up.",
		"status":  "provisioning",
	})
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
