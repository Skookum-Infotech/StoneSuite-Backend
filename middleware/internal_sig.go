package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"
)

// Header names a machine caller (the Cloudflare Cron Worker) signs internal
// requests with. The signature is
// hex(HMAC-SHA256(secret, method + " " + path + "\n" + ts + "\n" + body)), so
// a signature captured for one internal route can't be replayed on another.
const (
	InternalTimestampHeader = "X-Automation-Timestamp"
	InternalSignatureHeader = "X-Automation-Signature"
	// InternalSignatureMaxSkew is the replay window: a signed request older
	// (or further in the future) than this is rejected.
	InternalSignatureMaxSkew = 5 * time.Minute
)

// Errors returned by VerifyInternalSignature. They are deliberately coarse —
// the handler maps every one to the same 401 so a caller learns nothing.
var (
	ErrInternalSigMissing = errors.New("internal signature: missing timestamp or signature")
	ErrInternalSigStale   = errors.New("internal signature: timestamp outside allowed skew")
	ErrInternalSigInvalid = errors.New("internal signature: signature mismatch")
)

// SignInternal computes the signature VerifyInternalSignature expects. It is
// exported for tests and local tooling; the production signer is the Worker.
func SignInternal(secret, method, path, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method + " " + path + "\n"))
	mac.Write([]byte(ts))
	mac.Write([]byte("\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyInternalSignature checks a signed internal request: ts is unix
// seconds within InternalSignatureMaxSkew of now, and sig is the hex HMAC of
// method, path, ts and body under secret (see SignInternal), compared in
// constant time. An empty secret always fails, so a misconfigured deploy can
// never accept unsigned calls.
func VerifyInternalSignature(secret, method, path, ts, sig string, body []byte, now time.Time) error {
	if secret == "" || ts == "" || sig == "" {
		return ErrInternalSigMissing
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrInternalSigMissing
	}
	skew := now.Sub(time.Unix(secs, 0))
	if skew < -InternalSignatureMaxSkew || skew > InternalSignatureMaxSkew {
		return ErrInternalSigStale
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return ErrInternalSigInvalid
	}
	want, err := hex.DecodeString(SignInternal(secret, method, path, ts, body))
	if err != nil {
		return ErrInternalSigInvalid
	}
	if !hmac.Equal(got, want) {
		return ErrInternalSigInvalid
	}
	return nil
}

// ReplayGuard remembers recently accepted signatures so an intercepted signed
// request can't be replayed inside the InternalSignatureMaxSkew window. It is
// per-process, which is enough here: the app runs a single machine, and a
// replay that reached another machine would still only repeat idempotent work.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplayGuard returns an empty guard.
func NewReplayGuard() *ReplayGuard {
	return &ReplayGuard{seen: map[string]time.Time{}}
}

// FirstUse records sig and reports whether it had not been seen within the
// replay window. Expired entries are pruned on every call, so the map never
// holds more than one window's worth of signatures.
func (g *ReplayGuard) FirstUse(sig string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for s, at := range g.seen {
		if now.Sub(at) > 2*InternalSignatureMaxSkew {
			delete(g.seen, s)
		}
	}
	if _, dup := g.seen[sig]; dup {
		return false
	}
	g.seen[sig] = now
	return true
}
