package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/mfa"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// Two-step verification at sign-in. With it on, a correct password at
// TenantLogin mints no session: requireLoginCode opens a challenge and emails
// a code, and MFAVerify mints the session once that code is entered. See
// mfa_settings.go for turning it on and off.

// Machine-readable reasons the sign-in screen and Account Settings branch on.
const (
	mfaCodeFormat       = "mfa_code_format"       // not 6 digits; nothing was checked
	mfaCodeInvalid      = "mfa_code_invalid"      // wrong code; attemptsLeft says how many remain
	mfaChallengeExpired = "mfa_challenge_expired" // challenge used, replaced or timed out; start over
	mfaChallengeLocked  = "mfa_challenge_locked"  // too many wrong codes; start over
	mfaResendTooSoon    = "mfa_resend_too_soon"   // retryAfterSeconds says how long to wait
	mfaSendLimit        = "mfa_send_limit"        // too many codes sent recently
	mfaSendFailed       = "mfa_send_failed"       // the email could not be handed to notify
)

const (
	msgMFACodeFormat    = "Enter the 6-digit code from your email."
	msgMFASendFailed    = "We couldn't send your code right now. Please try again in a moment."
	msgMFASendLimit     = "Too many codes have been sent to this account. Please try again later."
	msgMFAResendTooSoon = "Please wait before asking for another code."
)

// mfaCodeTTLMinutes is mfa.CodeTTL as the whole minutes the email quotes.
var mfaCodeTTLMinutes = int(mfa.CodeTTL / time.Minute)

// newMFACode returns a fresh code and the hash stored in its place.
func newMFACode() (code, codeHash string, err error) {
	code, err = mfa.GenerateCode()
	if err != nil {
		return "", "", err
	}
	codeHash, err = mfa.HashCode(code)
	if err != nil {
		return "", "", err
	}
	return code, codeHash, nil
}

// codeSentFields is the part of every "a code is on its way" response the
// code-entry screens need: where it went and the timings to show.
func codeSentFields(email string) map[string]any {
	return map[string]any{
		"success":            true,
		"maskedEmail":        mfa.MaskEmail(email),
		"codeLength":         mfa.CodeLength,
		"expiresInSeconds":   mfa.WaitSeconds(mfa.CodeTTL),
		"resendAfterSeconds": mfa.WaitSeconds(mfa.ResendCooldown),
	}
}

// failResendTooSoon answers a send that came inside the cooldown.
func failResendTooSoon(w http.ResponseWriter, wait time.Duration) {
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"success": false, "code": mfaResendTooSoon, "message": msgMFAResendTooSoon,
		"retryAfterSeconds": mfa.WaitSeconds(wait),
	})
}

// failWrongCode answers a wrong code: 400 with the tries left, or 410 once
// that was the last try and the challenge is dead.
func failWrongCode(w http.ResponseWriter, r *http.Request, ch *tenancy.MFAChallenge, cp *tenancy.ControlPlane, lockedMsg string) {
	attempts, err := cp.RecordMFAFailedAttempt(r.Context(), ch.ID)
	if errors.Is(err, tenancy.ErrMFAChallengeNotFound) {
		failWithCode(w, http.StatusGone, lockedMsg, mfaChallengeLocked)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check the code.")
		return
	}
	left := mfa.AttemptsLeft(attempts)
	logSecurityEvent(r, "mfa_failed", "identity", ch.IdentityID, "purpose", ch.Purpose, "attempts_left", left)
	if left == 0 {
		logSecurityEvent(r, "mfa_locked", "identity", ch.IdentityID, "purpose", ch.Purpose)
		failWithCode(w, http.StatusGone, lockedMsg, mfaChallengeLocked)
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"success": false, "code": mfaCodeInvalid,
		"message":      "That code isn't right. Check the email and try again.",
		"attemptsLeft": left,
	})
}

// requireLoginCode stands between a password-verified login and its session:
// for an identity with two-step verification on it opens a challenge, emails
// the code and answers mfaRequired instead. Returns true once it has written
// the response. A login that already passed the code (MFAVerify) goes through.
func (h *TenantOps) requireLoginCode(w http.ResponseWriter, r *http.Request, identity *tenancy.Identity, at loginAttempt) bool {
	if !identity.MFAEnabled || at.codeVerified {
		return false
	}
	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return true
	}
	code, codeHash, err := newMFACode()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return true
	}
	_, err = h.CP.CreateMFAChallenge(r.Context(), identity.ID, mfa.PurposeLogin,
		mfa.HashChallengeToken(token), codeHash, at.rememberMe)
	if errors.Is(err, tenancy.ErrMFASendLimit) {
		logSecurityEvent(r, "mfa_send_limited", "identity", identity.ID, "purpose", mfa.PurposeLogin)
		failWithCode(w, http.StatusTooManyRequests, msgMFASendLimit, mfaSendLimit)
		return true
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "open sign-in code challenge", "identity", identity.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Login failed.")
		return true
	}
	name := tenantDisplayName(r.Context(), h.CP, h.Router, identity)
	if err := services.SendSignInCodeEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, name, code, mfaCodeTTLMinutes); err != nil {
		slog.ErrorContext(r.Context(), "sign-in code email failed", "identity", identity.ID, "error", err)
		failWithCode(w, http.StatusServiceUnavailable, msgMFASendFailed, mfaSendFailed)
		return true
	}
	logSecurityEvent(r, "mfa_code_sent", "identity", identity.ID, "purpose", mfa.PurposeLogin)

	resp := codeSentFields(identity.Email)
	resp["mfaRequired"] = true
	resp["challengeToken"] = token
	writeJSON(w, http.StatusOK, resp)
	return true
}

type mfaChallengeRequest struct {
	ChallengeToken string `json:"challengeToken"`
	Code           string `json:"code"`
}

// loginChallenge loads the live sign-in challenge a request's token names.
// Writes the failure itself and returns nil when there is none.
func (h *TenantOps) loginChallenge(w http.ResponseWriter, r *http.Request, req mfaChallengeRequest) *tenancy.MFAChallenge {
	if req.ChallengeToken == "" {
		fail(w, http.StatusBadRequest, "challengeToken is required.")
		return nil
	}
	ch, err := h.CP.MFAChallengeByToken(r.Context(), mfa.HashChallengeToken(req.ChallengeToken), mfa.PurposeLogin)
	if errors.Is(err, tenancy.ErrMFAChallengeNotFound) {
		failWithCode(w, http.StatusGone, "This code has expired. Sign in again to get a new one.", mfaChallengeExpired)
		return nil
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check the code.")
		return nil
	}
	return ch
}

// MFAVerify checks the emailed sign-in code and, when it is right, finishes
// the login exactly as TenantLogin would have without the code step.
// Path: POST /api/auth/mfa/verify
func (h *TenantOps) MFAVerify(w http.ResponseWriter, r *http.Request) {
	var req mfaChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	code := mfa.NormalizeCode(req.Code)
	if !mfa.ValidCodeFormat(code) {
		failWithCode(w, http.StatusBadRequest, msgMFACodeFormat, mfaCodeFormat)
		return
	}
	ch := h.loginChallenge(w, r, req)
	if ch == nil {
		return
	}
	if !mfa.CodeMatches(ch.CodeHash, code) {
		failWrongCode(w, r, ch, h.CP, "Too many incorrect codes. Sign in again to get a new one.")
		return
	}
	// Only one request can consume the challenge, so a code is good for one
	// session even if it is submitted twice at once.
	if err := h.CP.ConsumeMFAChallenge(r.Context(), ch.ID); err != nil {
		if errors.Is(err, tenancy.ErrMFAChallengeNotFound) {
			failWithCode(w, http.StatusGone, "This code has expired. Sign in again to get a new one.", mfaChallengeExpired)
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to check the code.")
		return
	}
	identity, err := h.CP.IdentityByID(r.Context(), ch.IdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	logSecurityEvent(r, "mfa_verified", "identity", identity.ID, "purpose", mfa.PurposeLogin)
	h.completeLogin(w, r, identity, loginAttempt{email: identity.Email, rememberMe: ch.RememberMe, codeVerified: true})
}

// MFAResend emails a new code for a sign-in challenge and restarts its expiry.
// Path: POST /api/auth/mfa/resend
func (h *TenantOps) MFAResend(w http.ResponseWriter, r *http.Request) {
	var req mfaChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ch := h.loginChallenge(w, r, req)
	if ch == nil {
		return
	}
	wait, err := mfa.CheckResend(ch.SendCount, ch.LastSentAt, time.Now())
	switch {
	case errors.Is(err, mfa.ErrTooManySends):
		failWithCode(w, http.StatusTooManyRequests, "You've asked for too many codes. Sign in again to start over.", mfaSendLimit)
		return
	case errors.Is(err, mfa.ErrResendTooSoon):
		failResendTooSoon(w, wait)
		return
	}

	code, codeHash, err := newMFACode()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to send a new code.")
		return
	}
	_, err = h.CP.ResendMFAChallenge(r.Context(), ch.ID, codeHash)
	switch {
	case errors.Is(err, tenancy.ErrMFAChallengeNotFound):
		failWithCode(w, http.StatusGone, "This code has expired. Sign in again to get a new one.", mfaChallengeExpired)
		return
	case errors.Is(err, tenancy.ErrMFASendLimit):
		logSecurityEvent(r, "mfa_send_limited", "identity", ch.IdentityID, "purpose", mfa.PurposeLogin)
		failWithCode(w, http.StatusTooManyRequests, msgMFASendLimit, mfaSendLimit)
		return
	case errors.Is(err, tenancy.ErrMFAResendRefused):
		failResendTooSoon(w, mfa.ResendCooldown)
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to send a new code.")
		return
	}

	identity, err := h.CP.IdentityByID(r.Context(), ch.IdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to send a new code.")
		return
	}
	name := tenantDisplayName(r.Context(), h.CP, h.Router, identity)
	if err := services.SendSignInCodeEmail(r.Context(), identity.TenantID, identity.ID, identity.Email, name, code, mfaCodeTTLMinutes); err != nil {
		slog.ErrorContext(r.Context(), "sign-in code resend email failed", "identity", identity.ID, "error", err)
		failWithCode(w, http.StatusServiceUnavailable, msgMFASendFailed, mfaSendFailed)
		return
	}
	logSecurityEvent(r, "mfa_code_sent", "identity", identity.ID, "purpose", mfa.PurposeLogin, "resend", true)
	writeJSON(w, http.StatusOK, codeSentFields(identity.Email))
}
