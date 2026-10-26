package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/mfa"
	"stonesuite-backend/middleware"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// Turning two-step verification on and off from Account Settings. Staff only:
// these routes live under /api/auth/mfa, which a portal token can never reach
// (RequireAuth confines it to /api/portal/), and mfaCaller refuses one anyway.
// SSO-only identities have no password step to add a code to, so the feature
// is unavailable to them.

const (
	mfaUnavailable    = "mfa_unavailable"
	mfaAlreadyEnabled = "mfa_already_enabled"
	invalidPassword   = "invalid_password"
)

const msgMFAUnavailable = "Two-step verification isn't available for accounts that sign in with single sign-on."

// mfaCaller loads the signed-in staff caller's identity. Writes the failure
// itself and returns nil when there is no usable caller.
func (h *TenantOps) mfaCaller(w http.ResponseWriter, r *http.Request) *tenancy.Identity {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil
	}
	if payload.Kind == middleware.KindPortal {
		failWithCode(w, http.StatusForbidden, "Two-step verification isn't available for customer portal accounts.", mfaUnavailable)
		return nil
	}
	identity, err := h.CP.IdentityByID(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load your account.")
		return nil
	}
	return identity
}

// mfaAvailable reports whether identity signs in with a password, the step a
// code is added to.
func mfaAvailable(identity *tenancy.Identity) bool {
	return identity.PasswordHash != ""
}

// MFAStatus reports the caller's two-step verification setting.
// Path: GET /api/auth/mfa
func (h *TenantOps) MFAStatus(w http.ResponseWriter, r *http.Request) {
	identity := h.mfaCaller(w, r)
	if identity == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"enabled":     identity.MFAEnabled,
		"available":   mfaAvailable(identity),
		"maskedEmail": mfa.MaskEmail(identity.Email),
	})
}

// MFAEnableStart emails the code that confirms turning two-step verification
// on, proving the inbox works before sign-in depends on it. Calling it again
// after the cooldown sends a fresh code and retires the previous one.
// Path: POST /api/auth/mfa/enable/start
func (h *TenantOps) MFAEnableStart(w http.ResponseWriter, r *http.Request) {
	identity := h.mfaCaller(w, r)
	if identity == nil {
		return
	}
	if !mfaAvailable(identity) {
		failWithCode(w, http.StatusBadRequest, msgMFAUnavailable, mfaUnavailable)
		return
	}
	if identity.MFAEnabled {
		failWithCode(w, http.StatusConflict, "Two-step verification is already on.", mfaAlreadyEnabled)
		return
	}
	latest, err := h.CP.LatestMFAChallenge(r.Context(), identity.ID, mfa.PurposeEnable)
	if err != nil && !errors.Is(err, tenancy.ErrMFAChallengeNotFound) {
		fail(w, http.StatusInternalServerError, "Failed to send a code.")
		return
	}
	if latest != nil {
		if wait, err := mfa.CheckResend(latest.SendCount, latest.LastSentAt, time.Now()); errors.Is(err, mfa.ErrResendTooSoon) {
			failResendTooSoon(w, wait)
			return
		}
	}

	// The enable flow is looked up by identity, so its token is never handed
	// out; it only fills the column every challenge row has.
	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to send a code.")
		return
	}
	code, codeHash, err := newMFACode()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to send a code.")
		return
	}
	_, err = h.CP.CreateMFAChallenge(r.Context(), identity.ID, mfa.PurposeEnable, mfa.HashChallengeToken(token), codeHash, false)
	if errors.Is(err, tenancy.ErrMFASendLimit) {
		logSecurityEvent(r, "mfa_send_limited", "identity", identity.ID, "purpose", mfa.PurposeEnable)
		failWithCode(w, http.StatusTooManyRequests, msgMFASendLimit, mfaSendLimit)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "open mfa enable challenge", "identity", identity.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to send a code.")
		return
	}
	name := tenantDisplayName(r.Context(), h.CP, h.Router, identity)
	if err := services.SendMFASetupCodeEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, name, code, mfaCodeTTLMinutes); err != nil {
		slog.ErrorContext(r.Context(), "mfa setup code email failed", "identity", identity.ID, "error", err)
		failWithCode(w, http.StatusServiceUnavailable, msgMFASendFailed, mfaSendFailed)
		return
	}
	logSecurityEvent(r, "mfa_code_sent", "identity", identity.ID, "purpose", mfa.PurposeEnable)
	writeJSON(w, http.StatusOK, codeSentFields(identity.Email))
}

// MFAEnableConfirm turns two-step verification on once the caller enters the
// code MFAEnableStart emailed. Path: POST /api/auth/mfa/enable/confirm
func (h *TenantOps) MFAEnableConfirm(w http.ResponseWriter, r *http.Request) {
	identity := h.mfaCaller(w, r)
	if identity == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	code := mfa.NormalizeCode(req.Code)
	if !mfa.ValidCodeFormat(code) {
		failWithCode(w, http.StatusBadRequest, msgMFACodeFormat, mfaCodeFormat)
		return
	}
	if !mfaAvailable(identity) {
		failWithCode(w, http.StatusBadRequest, msgMFAUnavailable, mfaUnavailable)
		return
	}
	if identity.MFAEnabled {
		failWithCode(w, http.StatusConflict, "Two-step verification is already on.", mfaAlreadyEnabled)
		return
	}

	const expiredMsg = "This code has expired. Send a new one."
	ch, err := h.CP.LatestMFAChallenge(r.Context(), identity.ID, mfa.PurposeEnable)
	if errors.Is(err, tenancy.ErrMFAChallengeNotFound) {
		failWithCode(w, http.StatusGone, expiredMsg, mfaChallengeExpired)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check the code.")
		return
	}
	if !mfa.CodeMatches(ch.CodeHash, code) {
		failWrongCode(w, r, ch, h.CP, "Too many incorrect codes. Send a new code to try again.")
		return
	}
	if err := h.CP.ConsumeMFAChallenge(r.Context(), ch.ID); err != nil {
		if errors.Is(err, tenancy.ErrMFAChallengeNotFound) {
			failWithCode(w, http.StatusGone, expiredMsg, mfaChallengeExpired)
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to check the code.")
		return
	}
	if err := h.CP.SetIdentityMFAEnabled(r.Context(), identity.ID, true); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to turn on two-step verification.")
		return
	}
	logSecurityEvent(r, "mfa_enabled", "identity", identity.ID)
	emailSent := h.sendMFAStatusNotice(r, identity, true)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "enabled": true, "emailSent": emailSent})
}

// MFADisable turns two-step verification off. It asks for the current
// password, so a session left open on a shared computer can't quietly drop
// the second step. Path: POST /api/auth/mfa/disable
func (h *TenantOps) MFADisable(w http.ResponseWriter, r *http.Request) {
	identity := h.mfaCaller(w, r)
	if identity == nil {
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.CurrentPassword == "" {
		fail(w, http.StatusBadRequest, "Enter your current password.")
		return
	}
	if !identity.MFAEnabled {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "enabled": false})
		return
	}
	// 400 rather than ChangePassword's 401: the frontend reads any 401 as an
	// expired session and would try to refresh it first.
	if err := bcrypt.CompareHashAndPassword([]byte(identity.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		logSecurityEvent(r, "mfa_disable_failed", "identity", identity.ID, "reason", "bad_password")
		failWithCode(w, http.StatusBadRequest, "Current password is incorrect.", invalidPassword)
		return
	}
	if err := h.CP.SetIdentityMFAEnabled(r.Context(), identity.ID, false); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to turn off two-step verification.")
		return
	}
	logSecurityEvent(r, "mfa_disabled", "identity", identity.ID)
	emailSent := h.sendMFAStatusNotice(r, identity, false)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "enabled": false, "emailSent": emailSent})
}

// sendMFAStatusNotice emails the "turned on/off" security notice. Best effort:
// the change already happened, so a failed send is not an error response; it is
// logged and reported to the caller as emailSent=false.
func (h *TenantOps) sendMFAStatusNotice(r *http.Request, identity *tenancy.Identity, enabled bool) bool {
	name := tenantDisplayName(r.Context(), h.CP, h.Router, identity)
	if err := services.SendMFAStatusEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, name, enabled); err != nil {
		slog.WarnContext(r.Context(), "mfa status notice email failed", "identity", identity.ID, "enabled", enabled, "error", err)
		return false
	}
	return true
}
