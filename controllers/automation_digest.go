package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// maxDigestPrefsBodyBytes caps the preferences request body.
const maxDigestPrefsBodyBytes = 4 << 10

// digestPrefsStore is the persistence the digest preferences API needs
// (consumer-side). Every method acts on one user: the caller.
type digestPrefsStore interface {
	// UserID resolves the tenant user row of the signed-in identity.
	UserID(ctx context.Context, identityID string) (string, error)
	Load(ctx context.Context, userID string) (automation.DigestPrefs, error)
	Save(ctx context.Context, userID string, p automation.DigestPrefs) error
}

// pgDigestPrefsStore implements digestPrefsStore on a tenant pool.
type pgDigestPrefsStore struct{ pool *pgxpool.Pool }

func (s pgDigestPrefsStore) UserID(ctx context.Context, identityID string) (string, error) {
	return workflow.UserIDByIdentity(ctx, s.pool, identityID)
}

func (s pgDigestPrefsStore) Load(ctx context.Context, userID string) (automation.DigestPrefs, error) {
	return automation.LoadDigestPrefs(ctx, s.pool, userID)
}

func (s pgDigestPrefsStore) Save(ctx context.Context, userID string, p automation.DigestPrefs) error {
	return automation.SaveDigestPrefs(ctx, s.pool, userID, p)
}

// AutomationDigestOps serves GET/PUT /api/tenant/automation/digest/me: a signed-in
// user's own daily digest preferences. It is self-service like the dashboard's
// widgets/me: the row is always the caller's (there is no id in the request, so
// there is nothing to enumerate), so it needs a login and a resolved tenant but
// no role permission.
type AutomationDigestOps struct {
	resolve func(r *http.Request) (digestPrefsStore, error)
}

// NewAutomationDigestOps builds the handler set backed by the resolved tenant pool.
func NewAutomationDigestOps() *AutomationDigestOps {
	return &AutomationDigestOps{resolve: func(r *http.Request) (digestPrefsStore, error) {
		pool, err := tenancy.PoolFromContext(r.Context())
		if err != nil {
			return nil, err
		}
		return pgDigestPrefsStore{pool: pool}, nil
	}}
}

// caller resolves the store and the caller's tenant user id, writing the failure itself.
func (h *AutomationDigestOps) caller(w http.ResponseWriter, r *http.Request) (digestPrefsStore, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	store, err := h.resolve(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	userID, err := store.UserID(r.Context(), payload.ID)
	if err != nil || userID == "" {
		slog.Error("resolve digest user", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to resolve tenant user.")
		return nil, "", false
	}
	return store, userID, true
}

// Get handles GET /api/tenant/automation/digest/me. A user who never saved
// preferences gets the defaults (digest off).
func (h *AutomationDigestOps) Get(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := h.caller(w, r)
	if !ok {
		return
	}
	prefs, err := store.Load(r.Context(), userID)
	if err != nil {
		slog.Error("load digest prefs", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load digest preferences.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "preferences": prefs})
}

// digestPrefsPatch is the PUT body. Every field is optional; one left out keeps
// the user's current value, so a client can flip a single switch.
type digestPrefsPatch struct {
	Enabled          *bool `json:"enabled"`
	SendHour         *int  `json:"sendHour"`
	IncludeInstalls    *bool `json:"includeInstalls"`
	IncludeVendorBills *bool `json:"includeVendorBills"`
	IncludeApprovals   *bool `json:"includeApprovals"`
	SkipEmpty          *bool `json:"skipEmpty"`
}

// apply overlays the fields the patch sets on current.
func (p digestPrefsPatch) apply(current automation.DigestPrefs) automation.DigestPrefs {
	if p.Enabled != nil {
		current.Enabled = *p.Enabled
	}
	if p.SendHour != nil {
		current.SendHour = *p.SendHour
	}
	if p.IncludeInstalls != nil {
		current.IncludeInstalls = *p.IncludeInstalls
	}
	if p.IncludeVendorBills != nil {
		current.IncludeVendorBills = *p.IncludeVendorBills
	}
	if p.IncludeApprovals != nil {
		current.IncludeApprovals = *p.IncludeApprovals
	}
	if p.SkipEmpty != nil {
		current.SkipEmpty = *p.SkipEmpty
	}
	return current
}

// empty reports whether the patch sets nothing.
func (p digestPrefsPatch) empty() bool { return p == digestPrefsPatch{} }

// Put handles PUT /api/tenant/automation/digest/me.
func (h *AutomationDigestOps) Put(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var patch digestPrefsPatch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDigestPrefsBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil || patch.empty() {
		fail(w, http.StatusBadRequest, "Body must set at least one of enabled, sendHour, includeInstalls, includeVendorBills, includeApprovals, skipEmpty.")
		return
	}
	current, err := store.Load(r.Context(), userID)
	if err != nil {
		slog.Error("load digest prefs", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load digest preferences.")
		return
	}
	next := patch.apply(current)
	if err := store.Save(r.Context(), userID, next); err != nil {
		if errors.Is(err, automation.ErrInvalidDigestPrefs) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("save digest prefs", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to save digest preferences.")
		return
	}
	slog.Info("automation_digest_prefs_changed", "user", userID, "enabled", next.Enabled, "send_hour", next.SendHour)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "preferences": next})
}
