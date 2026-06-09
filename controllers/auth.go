package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// resetTokenExpiry is how long a password-reset link stays valid.
const resetTokenExpiry = 24 * time.Hour

// resetPasswordLink returns the full frontend URL the user clicks to reset.
func resetPasswordLink(token string) string {
	return frontendBase() + "/auth/reset-password?token=" + token
}

// ForgotPassword handles POST /api/auth/forgot-password.
// Accepts { email }, generates a one-time reset token, and emails a link.
// Always returns 200 — we never reveal whether the address exists in our system.
func (h *TenantOps) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		fail(w, http.StatusBadRequest, "A valid email is required.")
		return
	}

	// Look up the identity — if not found, respond 200 anyway so we don't
	// leak whether the address is registered.
	identity, err := h.CP.IdentityByEmail(r.Context(), req.Email)
	if errors.Is(err, tenancy.ErrIdentityNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": "If an account with that address exists, a reset link has been sent.",
		})
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to process request.")
		return
	}

	// Generate a cryptographically random token and persist it.
	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate reset token.")
		return
	}
	expiry := time.Now().Add(resetTokenExpiry)
	if err := h.CP.SetIdentityPasswordSetupToken(r.Context(), identity.ID, token, expiry); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to store reset token.")
		return
	}

	// Send the email in the background; don't block the HTTP response.
	link := resetPasswordLink(token)
	go func() {
		if err := services.SendPasswordResetEmail(identity.Email, identity.FullName, link); err != nil {
			log.Printf("forgot-password: email to %s failed: %v", identity.Email, err)
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "If an account with that address exists, a reset link has been sent.",
	})
}

// ValidateResetToken handles GET /api/auth/reset-password/{token}.
// Returns the email address associated with the token so the UI can display it.
func (h *TenantOps) ValidateResetToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	token := r.PathValue("token")
	if token == "" {
		fail(w, http.StatusBadRequest, "Missing token.")
		return
	}

	identity, err := h.CP.IdentityByPasswordToken(r.Context(), token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This reset link is invalid or has expired.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"valid":   true,
		"email":   identity.Email,
	})
}

// ResetPassword handles POST /api/auth/reset-password.
// Accepts { token, newPassword }, validates the token, and sets the new password.
func (h *TenantOps) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
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
		fail(w, http.StatusBadRequest, "A reset token is required.")
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

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}

	if err := h.CP.SetIdentityPassword(r.Context(), identity.ID, string(hash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update password.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Password updated. You can now sign in with your new password.",
	})
}
