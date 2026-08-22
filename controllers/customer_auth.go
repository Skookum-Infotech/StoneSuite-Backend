package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// customerInviteExpiry is the TTL for a customer-portal invitation.
const customerInviteExpiry = 48 * time.Hour

// customerInviteLink builds the public frontend URL an invited customer
// follows to set their password and activate their portal login.
func customerInviteLink(tenantSlug, token string) string {
	return frontendBase() + "/portal/accept-invite?tenant=" + tenantSlug + "&token=" + token
}

// CustomerAuthOps groups the customer-portal auth handlers (login, accept
// invite). Deps are injected for testability, mirroring TenantOps.
type CustomerAuthOps struct {
	CP     *tenancy.ControlPlane
	Router *tenancy.Router
}

// NewCustomerAuthOps constructs the handler group.
func NewCustomerAuthOps(cp *tenancy.ControlPlane, router *tenancy.Router) *CustomerAuthOps {
	return &CustomerAuthOps{CP: cp, Router: router}
}

type customerIdentityRow struct {
	ID           string
	CustomerID   int
	Email        string
	PasswordHash string
	FullName     string
	Status       string
}

// generateCustomerJWT signs a customer-portal token. Its claim shape is
// deliberately disjoint from generateTenantJWT's (see middleware.RequireAuth
// and middleware.RequireCustomerAuth) so a customer session can never
// authenticate a staff route or vice versa.
func generateCustomerJWT(customerIdentityID string, customerID int, tenantID string, d time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"principal_type": middleware.CustomerPrincipalType,
		"sub":            customerIdentityID,
		"customer_id":    strconv.Itoa(customerID),
		"tenant_id":      tenantID,
		"exp":            time.Now().Add(d).Unix(),
		"iat":            time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(config.AppConfig.JWTSecret))
}

// setCustomerAuthCookies writes the customer_auth_token cookie, mirroring
// setAuthCookies but under a distinctly-named cookie so a staff session and a
// customer session in the same browser can never collide.
func setCustomerAuthCookies(w http.ResponseWriter, token string, d time.Duration) error {
	sameSite := http.SameSiteLaxMode
	if config.AppConfig.CookieSameSite == "none" {
		sameSite = http.SameSiteNoneMode
	}
	secure := config.AppConfig.IsProduction()

	http.SetCookie(w, &http.Cookie{
		Name:     "customer_auth_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: sameSite,
		MaxAge:   int(d.Seconds()),
	})

	if config.AppConfig.CookieSameSite == "none" {
		csrfToken, err := randomToken()
		if err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "csrf_token",
			Value:    csrfToken,
			Path:     "/",
			HttpOnly: false,
			Secure:   secure,
			SameSite: sameSite,
			MaxAge:   int(d.Seconds()),
		})
	}
	return nil
}

type customerLoginRequest struct {
	TenantSlug string `json:"tenantSlug"`
	Email      string `json:"email"`
	Password   string `json:"password"`
}

// Login authenticates an external customer against one tenant's portal and
// issues a customer JWT. tenantSlug + email + password are all required —
// unlike staff login, there is no global identity registry to resolve a
// tenant from an email alone.
func (h *CustomerAuthOps) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req customerLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.TenantSlug = strings.TrimSpace(req.TenantSlug)
	req.Email = strings.TrimSpace(req.Email)
	if req.TenantSlug == "" || req.Email == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "tenantSlug, email, and password are required.")
		return
	}

	const genericFail = "Invalid email or password."

	tenant, err := h.CP.TenantBySlug(r.Context(), req.TenantSlug)
	if errors.Is(err, tenancy.ErrTenantNotFound) {
		logSecurityEvent(r, "customer_login_failed", "tenant_slug", req.TenantSlug, "reason", "unknown_tenant")
		fail(w, http.StatusUnauthorized, genericFail)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	if !tenant.Servable() {
		logSecurityEvent(r, "customer_login_failed", "tenant_slug", req.TenantSlug, "reason", "unservable_tenant")
		fail(w, http.StatusUnauthorized, genericFail)
		return
	}

	pool, err := h.Router.PoolFor(r.Context(), tenant)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to connect to tenant database.")
		return
	}

	var row customerIdentityRow
	err = pool.QueryRow(r.Context(), `
		SELECT id, customer_id, email, password_hash, full_name, status
		FROM customer_identities WHERE LOWER(email) = LOWER($1)`, req.Email,
	).Scan(&row.ID, &row.CustomerID, &row.Email, &row.PasswordHash, &row.FullName, &row.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		logSecurityEvent(r, "customer_login_failed", "email", req.Email, "tenant_id", tenant.ID, "reason", "unknown_identity")
		fail(w, http.StatusUnauthorized, genericFail)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(req.Password)) != nil {
		logSecurityEvent(r, "customer_login_failed", "email", req.Email, "tenant_id", tenant.ID, "reason", "bad_password")
		fail(w, http.StatusUnauthorized, genericFail)
		return
	}
	if row.Status == "disabled" {
		fail(w, http.StatusForbidden, "This customer account has been disabled.")
		return
	}
	if row.Status != "active" {
		// e.g. still "invited" — password_hash would be empty and the bcrypt
		// compare above would already have failed, but guard explicitly in
		// case that ever changes.
		fail(w, http.StatusUnauthorized, genericFail)
		return
	}

	d, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		d = time.Hour
	}
	token, err := generateCustomerJWT(row.ID, row.CustomerID, tenant.ID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}
	if err := setCustomerAuthCookies(w, token, d); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to establish session.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"token":     token,
		"expiresAt": time.Now().Add(d).UnixMilli(),
		"customer": map[string]any{
			"id":    row.ID,
			"email": row.Email,
			"name":  row.FullName,
		},
	})
}

type customerAcceptInviteRequest struct {
	TenantSlug string `json:"tenantSlug"`
	Token      string `json:"token"`
	FullName   string `json:"fullName"`
	Password   string `json:"password"`
}

// AcceptInvite consumes a portal-invite token (issued by staff via
// PortalInvite) and sets the customer's password, activating their login.
func (h *CustomerAuthOps) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req customerAcceptInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.TenantSlug = strings.TrimSpace(req.TenantSlug)
	req.Token = strings.TrimSpace(req.Token)
	if req.TenantSlug == "" || req.Token == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "tenantSlug, token, and password are required.")
		return
	}
	if len(req.Password) < 8 {
		fail(w, http.StatusBadRequest, "Password must be at least 8 characters.")
		return
	}

	tenant, err := h.CP.TenantBySlug(r.Context(), req.TenantSlug)
	if errors.Is(err, tenancy.ErrTenantNotFound) {
		fail(w, http.StatusBadRequest, "Invalid or expired invitation.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to accept invitation.")
		return
	}
	if !tenant.Servable() {
		fail(w, http.StatusBadRequest, "Invalid or expired invitation.")
		return
	}
	pool, err := h.Router.PoolFor(r.Context(), tenant)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to connect to tenant database.")
		return
	}

	var identityID string
	err = pool.QueryRow(r.Context(), `
		SELECT id FROM customer_identities
		WHERE invite_token = $1 AND status = 'invited' AND invite_token_expiry > NOW()`, req.Token,
	).Scan(&identityID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusBadRequest, "Invalid or expired invitation.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to accept invitation.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}
	fullName := strings.TrimSpace(req.FullName)

	_, err = pool.Exec(r.Context(), `
		UPDATE customer_identities SET
			password_hash = $1, status = 'active', invite_token = NULL, invite_token_expiry = NULL,
			full_name = CASE WHEN $2 = '' THEN full_name ELSE $2 END, updated_at = NOW()
		WHERE id = $3`,
		string(hash), fullName, identityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to activate account.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Account activated. You can now log in.",
	})
}
