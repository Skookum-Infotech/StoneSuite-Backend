package controllers

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/tenancy"
)

// dummyPasswordHash is a cost-10 bcrypt hash (the cost every real password is
// stored at) of a value no one knows. Comparing against it when the account
// doesn't exist or has no password makes those failures take as long as a
// wrong password, so response time no longer reveals which emails exist.
const dummyPasswordHash = "$2a$10$jFKZ3eNu.RjnDv0Sg5BLmeKtC0JehleIG.VkC2lo8qPP5wY0dTolS"

// Backoff policy: loginLockThreshold consecutive failures lock password login
// for loginLockBase, doubling per further failure, capped at loginLockMax.
const (
	loginLockThreshold = 5
	loginLockBase      = time.Minute
	loginLockMax       = time.Hour
)

// loginThrottle returns the per-account backoff policy.
func loginThrottle() tenancy.LoginThrottle {
	return tenancy.LoginThrottle{Threshold: loginLockThreshold, Base: loginLockBase, Max: loginLockMax}
}

// msgTooManyLoginAttempts is the 429 body while an account is backed off.
const msgTooManyLoginAttempts = "Too many failed sign-in attempts. Please wait a few minutes and try again."

// burnPasswordCheck spends one bcrypt comparison's worth of time on a login
// that is going to fail without a real comparison.
func burnPasswordCheck(password string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password)) // always a mismatch
}

// refuseIfLoginLocked answers 429 (with Retry-After) and returns true when the
// identity is in backoff. A lookup error fails open: the per-IP limit still
// applies, and refusing every login on a control-plane blip would be worse.
func refuseIfLoginLocked(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, identityID, event string) bool {
	until, locked, err := cp.LoginLockedUntil(r.Context(), identityID)
	if err != nil {
		slog.Error("login lock check failed", "identity", identityID, "err", err)
		return false
	}
	if !locked {
		return false
	}
	logSecurityEvent(r, event, "identity", identityID, "reason", "account_backoff")
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(time.Until(until))))
	fail(w, http.StatusTooManyRequests, msgTooManyLoginAttempts)
	return true
}

// retryAfterSeconds rounds a remaining lock duration up to whole seconds (min 1).
func retryAfterSeconds(d time.Duration) int {
	return int(math.Max(1, math.Ceil(d.Seconds())))
}

// noteLoginFailure counts a wrong password toward the identity's backoff.
func noteLoginFailure(r *http.Request, cp *tenancy.ControlPlane, identityID string) {
	if err := cp.RecordLoginFailure(r.Context(), identityID, loginThrottle()); err != nil {
		slog.Error("record login failure", "identity", identityID, "err", err)
	}
}

// noteLoginSuccess clears the identity's backoff after a correct password.
func noteLoginSuccess(r *http.Request, cp *tenancy.ControlPlane, identityID string) {
	if err := cp.ResetLoginFailures(r.Context(), identityID); err != nil {
		slog.Error("reset login failures", "identity", identityID, "err", err)
	}
}
