package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/middleware"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// onboardingCustomerKey is the workflow key whose field definitions drive the
// onboarding form and represent onboarded customers in the owner workspace.
const onboardingCustomerKey = "customer"

// ---- shared finalize -------------------------------------------------------

// finalizeOnboarding creates the customer's (password-pending) identity, kicks
// off tenant provisioning, records the customer in the owner's Customer
// workflow (best-effort), and emails a password-setup link. Returns the setup
// link so callers can surface it as a dev fallback. Used by both the immediate
// onboard path and the approval path.
func (h *TenantOps) finalizeOnboarding(ctx context.Context, tenant *tenancy.Tenant, formData map[string]any) (string, error) {
	email := formStr(formData, "super_admin_email")
	fullName := formStr(formData, "super_admin_name")
	if email == "" {
		return "", errors.New("super_admin_email is required")
	}

	// Create a setup-pending identity (empty password → cannot sign in yet).
	identity, err := h.CP.CreateIdentity(ctx, tenant.ID, email, "", fullName, false)
	if err != nil {
		// Likely already exists (re-onboard) — reuse it.
		identity, err = h.CP.IdentityByEmail(ctx, email)
		if err != nil {
			return "", err
		}
	}

	token, err := randomToken()
	if err != nil {
		return "", err
	}
	// Password-setup links live a bit longer than invites.
	if err := h.CP.SetIdentityPasswordSetupToken(ctx, identity.ID, token, time.Now().Add(72*time.Hour)); err != nil {
		return "", err
	}

	h.Prov.Enqueue(provisioning.Job{
		TenantID:   tenant.ID,
		Slug:       tenant.Slug,
		IdentityID: identity.ID,
		Email:      identity.Email,
		FullName:   identity.FullName,
	})

	// Best-effort: record the onboarded customer in the owner's Customer workflow.
	h.recordOwnerCustomer(ctx, formData)

	link := setupLink(token)
	if err := services.SendPasswordSetupEmail(email, fullName, link); err != nil {
		log.Printf("password-setup email to %s not sent (link still valid): %v", email, err)
	}
	return link, nil
}

// reviewOnboarding approves or rejects a submitted onboarding application.
func (h *TenantOps) reviewOnboarding(w http.ResponseWriter, r *http.Request, admin middleware.UserContextPayload, tenantID, action string) {
	tenant, err := h.CP.TenantByID(r.Context(), tenantID)
	if err != nil {
		fail(w, http.StatusNotFound, "Tenant not found.")
		return
	}

	if action == "reject" {
		if err := h.CP.SetTenantStatus(r.Context(), tenantID, tenancy.StatusRejected); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to reject application.")
			return
		}
		_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenantID, "tenant.rejected", "{}")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Application rejected."})
		return
	}

	// approve
	formData := map[string]any{}
	if tenant.Metadata != "" {
		_ = json.Unmarshal([]byte(tenant.Metadata), &formData)
	}
	setup, err := h.finalizeOnboarding(r.Context(), tenant, formData)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Approval failed: "+err.Error())
		return
	}
	_ = h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, tenantID, "tenant.approved", "{}")
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": "Approved. Provisioning started.", "passwordSetupLink": setup,
	})
}

// ---- owner Customer workflow helpers ---------------------------------------

// ownerCustomerDef resolves the platform-owner tenant, its DB pool, and its
// "customer" workflow definition.
func (h *TenantOps) ownerCustomerDef(ctx context.Context) (*workflow.Definition, *tenancy.Tenant, *pgxpool.Pool, error) {
	owner, err := h.CP.PlatformOwnerTenant(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if !owner.Servable() {
		return nil, owner, nil, errors.New("owner workspace not provisioned")
	}
	pool, err := h.Router.PoolFor(ctx, owner)
	if err != nil {
		return nil, owner, nil, err
	}
	wfs, err := workflow.ListWorkflows(ctx, pool)
	if err != nil {
		return nil, owner, pool, err
	}
	var wfID string
	for _, wf := range wfs {
		if strings.EqualFold(wf.Key, onboardingCustomerKey) {
			wfID = wf.ID
			break
		}
	}
	if wfID == "" {
		return nil, owner, pool, errors.New("customer workflow not found")
	}
	def, err := workflow.LoadDefinition(ctx, pool, wfID)
	if err != nil {
		return nil, owner, pool, err
	}
	return def, owner, pool, nil
}

// recordOwnerCustomer inserts a record into the owner's Customer workflow with
// the onboarding values. Best-effort: failures are logged, never fatal.
func (h *TenantOps) recordOwnerCustomer(ctx context.Context, formData map[string]any) {
	def, _, pool, err := h.ownerCustomerDef(ctx)
	if err != nil {
		log.Printf("skip owner customer record: %v", err)
		return
	}
	// Keep only keys the Customer workflow defines (validation rejects unknowns).
	custom := map[string]any{}
	for _, f := range def.Fields {
		if v, ok := formData[f.Key]; ok {
			custom[f.Key] = v
		}
	}
	core := map[string]any{"name": formStr(formData, "company_name")}
	if _, err := workflow.NewEngine().CreateRecord(ctx, pool, def, "", "", core, custom); err != nil {
		log.Printf("skip owner customer record: create failed: %v", err)
	}
}

// ---- public endpoints (no auth) --------------------------------------------

// FormSchema returns the owner Customer workflow's field definitions so the
// onboarding form (public + owner) can render dynamic fields. No auth.
// Path: GET /api/onboarding/form-schema
func (h *TenantOps) FormSchema(w http.ResponseWriter, r *http.Request) {
	def, _, _, err := h.ownerCustomerDef(r.Context())
	if err != nil {
		// Degrade gracefully: an empty schema lets the base form still render.
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "fields": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fields": def.Fields})
}

// GetApply returns invite validity + any prefilled data for the public form.
// Path: GET /api/onboarding/apply/{token}
func (h *TenantOps) GetApply(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/api/onboarding/apply/")
	if token == "" {
		fail(w, http.StatusBadRequest, "Missing token.")
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
	prefill := map[string]any{}
	if tenant.Metadata != "" {
		_ = json.Unmarshal([]byte(tenant.Metadata), &prefill)
	}
	valid := inv.Status == "pending" && time.Now().Before(inv.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "valid": valid, "status": inv.Status,
		"contactEmail": inv.ContactEmail, "tenantName": tenant.DisplayName,
		"prefill": prefill,
	})
}

type submitApplyRequest struct {
	Token    string         `json:"token"`
	FormData map[string]any `json:"formData"`
}

// SubmitApply records a customer's filled onboarding form for owner approval.
// Path: POST /api/onboarding/apply
func (h *TenantOps) SubmitApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req submitApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FormData == nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
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
	if formStr(req.FormData, "company_name") == "" || formStr(req.FormData, "super_admin_email") == "" {
		fail(w, http.StatusBadRequest, "Company name and super-admin email are required.")
		return
	}

	if md, mErr := json.Marshal(req.FormData); mErr == nil {
		if err := h.CP.SetTenantMetadata(r.Context(), inv.TenantID, string(md)); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to save submission.")
			return
		}
	}
	if err := h.CP.SetTenantStatus(r.Context(), inv.TenantID, tenancy.StatusSubmitted); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update status.")
		return
	}
	_ = h.CP.MarkInviteAccepted(r.Context(), inv.ID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"message": "Thanks! Your application was submitted and is pending review.",
	})
}

// GetSetPassword validates a password-setup token and returns who it's for.
// Path: GET /api/onboarding/set-password/{token}
func (h *TenantOps) GetSetPassword(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/api/onboarding/set-password/")
	if token == "" {
		fail(w, http.StatusBadRequest, "Missing token.")
		return
	}
	identity, err := h.CP.IdentityByPasswordToken(r.Context(), token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This link is invalid or has expired.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "valid": true, "email": identity.Email, "fullName": identity.FullName,
	})
}

type setPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// SetPassword sets an onboarded customer's initial password.
// Path: POST /api/onboarding/set-password
func (h *TenantOps) SetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req setPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if req.Token == "" || len(req.Password) < 8 {
		fail(w, http.StatusBadRequest, "A token and a password (min 8 chars) are required.")
		return
	}
	identity, err := h.CP.IdentityByPasswordToken(r.Context(), req.Token)
	if err != nil {
		fail(w, http.StatusBadRequest, "This link is invalid or has expired.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to hash password.")
		return
	}
	if err := h.CP.SetIdentityPassword(r.Context(), identity.ID, string(hash)); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to set password.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": "Password set. You can now sign in.", "email": identity.Email,
	})
}
