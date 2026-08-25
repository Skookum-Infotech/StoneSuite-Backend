// controllers/customer_portal_admin.go
package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/authz"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// CustomerPortalAdminOps handles the staff-facing side of onboarding a
// customer to their self-serve portal: inviting an existing CRM customer
// record to set a password and log in. Reuses ResourceCustomer:update — this
// is modifying access on an existing customer, not a distinct capability.
//
// Route: POST /api/tenant/crm/customer/records/{id}/portal-invite
type CustomerPortalAdminOps struct {
	crm *CRMOps
}

// NewCustomerPortalAdminOps constructs the handler group.
func NewCustomerPortalAdminOps() *CustomerPortalAdminOps {
	return &CustomerPortalAdminOps{crm: NewCRMOps()}
}

type portalInviteRequest struct {
	Email    string `json:"email"`
	FullName string `json:"fullName"`
}

// PortalInvite POST /api/tenant/crm/customer/records/{id}/portal-invite
func (h *CustomerPortalAdminOps) PortalInvite(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	_, pool, key, _, ok := h.crm.authCRMByRecordID(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	if key != "customer" {
		fail(w, http.StatusBadRequest, "Only customer records can be invited to the portal.")
		return
	}

	var req portalInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.FullName = strings.TrimSpace(req.FullName)
	if req.Email == "" {
		fail(w, http.StatusBadRequest, "email is required.")
		return
	}

	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}

	var custInternalID int
	err = pool.QueryRow(r.Context(),
		`SELECT customer_id FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, recordID,
	).Scan(&custInternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return
	}

	var existingStatus string
	err = pool.QueryRow(r.Context(),
		`SELECT status FROM customer_identities WHERE customer_id = $1`, custInternalID,
	).Scan(&existingStatus)
	identityExists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusInternalServerError, "Failed to check existing portal login.")
		return
	}
	if existingStatus == "active" {
		fail(w, http.StatusConflict, "This customer already has an active portal login.")
		return
	}

	token, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate invite token.")
		return
	}
	expiry := time.Now().Add(customerInviteExpiry)

	if !identityExists {
		_, err = pool.Exec(r.Context(), `
			INSERT INTO customer_identities (customer_id, email, full_name, status, invite_token, invite_token_expiry)
			VALUES ($1, $2, $3, 'invited', $4, $5)`,
			custInternalID, req.Email, req.FullName, token, expiry)
	} else {
		_, err = pool.Exec(r.Context(), `
			UPDATE customer_identities SET
				email = $1, full_name = $2, status = 'invited',
				invite_token = $3, invite_token_expiry = $4, updated_at = NOW()
			WHERE customer_id = $5`,
			req.Email, req.FullName, token, expiry, custInternalID)
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create invitation.")
		return
	}

	link := customerInviteLink(tenant.Slug, token)
	if err := services.SendCustomerPortalInviteEmail(req.Email, req.FullName, tenant.DisplayName, link); err != nil {
		log.Printf("customer portal invite email to %s failed (invite still valid): %v", req.Email, err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Invitation sent."})
}
