package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/database"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/services"

	"golang.org/x/crypto/bcrypt"
)

func CustomersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		listCustomers(w, r)
	case http.MethodPost:
		createCustomer(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed. Use GET or POST."})
	}
}

func CustomerHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/api/customers/")
	path = strings.Trim(path, "/")
	segments := strings.Split(path, "/")

	if len(segments) == 0 || segments[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Customer route not found."})
		return
	}

	customerID := segments[0]

	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			getCustomer(w, r, customerID)
		case http.MethodPut:
			updateCustomer(w, r, customerID)
		case http.MethodDelete:
			deleteCustomer(w, r, customerID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed."})
		}
		return
	}

	switch segments[1] {
	case "contacts":
		handleContacts(w, r, customerID, segments[2:])
	case "invites":
		handleInvites(w, r, customerID, segments[2:])
	case "audit":
		if r.Method == http.MethodGet && len(segments) == 2 {
			listAuditLogs(w, r, customerID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Audit route not found."})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Customer sub-route not found."})
	}
}

func listCustomers(w http.ResponseWriter, r *http.Request) {
	customers, err := database.GetAllCustomers()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load customers."})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.CustomerListResponse{Success: true, Customers: customers})
}

func createCustomer(w http.ResponseWriter, r *http.Request) {
	var req models.CreateCustomerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.SuperAdminName = strings.TrimSpace(req.SuperAdminName)
	req.SuperAdminEmail = strings.ToLower(strings.TrimSpace(req.SuperAdminEmail))

	if req.Name == "" || req.SuperAdminName == "" || req.SuperAdminEmail == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Customer name and super admin contact name/email are required."})
		return
	}
	if !emailRegex.MatchString(req.SuperAdminEmail) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Super admin email is not valid."})
		return
	}

	customer, err := database.CreateCustomer(req.Name, req.Industry, req.Website)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer record."})
		return
	}

	_, err = database.CreateCustomerContact(customer.ID, req.SuperAdminName, req.SuperAdminEmail, req.SuperAdminPhone, "super_admin")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer contact."})
		return
	}

	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customer.ID, "", actor.ID, actor.Email, "customer_created", fmt.Sprintf("Created customer %s with super admin %s", customer.Name, req.SuperAdminEmail))

	createdCustomer, err := database.GetCustomerByIDWithContacts(customer.ID)
	if err != nil || createdCustomer == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to retrieve created customer."})
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Success  bool             `json:"success"`
		Message  string           `json:"message"`
		Customer *models.Customer `json:"customer"`
	}{Success: true, Message: "Customer created successfully.", Customer: createdCustomer})
}

func getCustomer(w http.ResponseWriter, r *http.Request, customerID string) {
	customer, err := database.GetCustomerByIDWithContacts(customerID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to retrieve customer."})
		return
	}
	if customer == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Customer not found."})
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Success  bool             `json:"success"`
		Customer *models.Customer `json:"customer"`
	}{Success: true, Customer: customer})
}

func updateCustomer(w http.ResponseWriter, r *http.Request, customerID string) {
	var req models.UpdateCustomerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}

	updated, err := database.UpdateCustomer(customerID, req.Name, req.Industry, req.Website, req.Status)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to update customer."})
		return
	}
	if updated == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Customer not found."})
		return
	}

	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, "", actor.ID, actor.Email, "customer_updated", fmt.Sprintf("Updated customer fields for %s", updated.Name))

	_ = json.NewEncoder(w).Encode(struct {
		Success  bool             `json:"success"`
		Message  string           `json:"message"`
		Customer *models.Customer `json:"customer"`
	}{Success: true, Message: "Customer updated successfully.", Customer: updated})
}

func deleteCustomer(w http.ResponseWriter, r *http.Request, customerID string) {
	err := database.DeleteCustomer(customerID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to delete customer."})
		return
	}
	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, "", actor.ID, actor.Email, "customer_deleted", "Deleted customer record.")
	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: true, Message: "Customer deleted successfully."})
}

func handleContacts(w http.ResponseWriter, r *http.Request, customerID string, segments []string) {
	if len(segments) == 0 || segments[0] == "" {
		switch r.Method {
		case http.MethodGet:
			listCustomerContacts(w, r, customerID)
		case http.MethodPost:
			createContact(w, r, customerID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed."})
		}
		return
	}

	contactID := segments[0]
	switch r.Method {
	case http.MethodPut:
		updateContact(w, r, customerID, contactID)
	case http.MethodDelete:
		deleteContact(w, r, customerID, contactID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed."})
	}
}

func listCustomerContacts(w http.ResponseWriter, r *http.Request, customerID string) {
	contacts, err := database.GetCustomerContacts(customerID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load customer contacts."})
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Success  bool                     `json:"success"`
		Contacts []models.CustomerContact `json:"contacts"`
	}{Success: true, Contacts: contacts})
}

func createContact(w http.ResponseWriter, r *http.Request, customerID string) {
	var req models.CreateContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.FullName == "" || req.Email == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact full name and email are required."})
		return
	}
	if !emailRegex.MatchString(req.Email) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact email is not valid."})
		return
	}

	contact, err := database.CreateCustomerContact(customerID, req.FullName, req.Email, req.Phone, req.Role)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer contact."})
		return
	}

	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, "", actor.ID, actor.Email, "contact_created", fmt.Sprintf("Created contact %s", contact.Email))

	_ = json.NewEncoder(w).Encode(struct {
		Success bool                    `json:"success"`
		Contact *models.CustomerContact `json:"contact"`
	}{Success: true, Contact: contact})
}

func updateContact(w http.ResponseWriter, r *http.Request, customerID, contactID string) {
	var req models.UpdateContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}

	contact, err := database.GetCustomerContactByID(contactID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load contact."})
		return
	}
	if contact == nil || contact.CustomerID != customerID {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact not found."})
		return
	}
	if req.Email != "" {
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if !emailRegex.MatchString(req.Email) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact email is not valid."})
			return
		}
	}

	updated, err := database.UpdateCustomerContact(contactID, req.FullName, req.Email, req.Phone, req.Role)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to update contact."})
		return
	}

	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, "", actor.ID, actor.Email, "contact_updated", fmt.Sprintf("Updated contact %s", updated.Email))

	_ = json.NewEncoder(w).Encode(struct {
		Success bool                    `json:"success"`
		Contact *models.CustomerContact `json:"contact"`
	}{Success: true, Contact: updated})
}

func deleteContact(w http.ResponseWriter, r *http.Request, customerID, contactID string) {
	contact, err := database.GetCustomerContactByID(contactID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load contact."})
		return
	}
	if contact == nil || contact.CustomerID != customerID {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact not found."})
		return
	}
	if err := database.DeleteCustomerContact(contactID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to delete contact."})
		return
	}

	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, "", actor.ID, actor.Email, "contact_deleted", fmt.Sprintf("Deleted contact %s", contact.Email))

	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: true, Message: "Contact deleted successfully."})
}

func handleInvites(w http.ResponseWriter, r *http.Request, customerID string, segments []string) {
	if len(segments) == 0 || segments[0] == "" {
		switch r.Method {
		case http.MethodGet:
			listInvites(w, r, customerID)
		case http.MethodPost:
			sendInvite(w, r, customerID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed."})
		}
		return
	}

	inviteID := segments[0]
	switch r.Method {
	case http.MethodGet:
		getInvite(w, r, customerID, inviteID)
	case http.MethodDelete:
		revokeInvite(w, r, customerID, inviteID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed."})
	}
}

func listInvites(w http.ResponseWriter, r *http.Request, customerID string) {
	invites, err := database.ListCustomerInvites(customerID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load invites."})
		return
	}
	_ = json.NewEncoder(w).Encode(models.InviteListResponse{Success: true, Invites: invites})
}

func sendInvite(w http.ResponseWriter, r *http.Request, customerID string) {
	var req models.SendInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}
	if req.ContactID == "" && req.ContactEmail == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact ID or contact email is required."})
		return
	}
	if req.ContactEmail != "" {
		req.ContactEmail = strings.ToLower(strings.TrimSpace(req.ContactEmail))
		if !emailRegex.MatchString(req.ContactEmail) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact email is not valid."})
			return
		}
	}

	var contact *models.CustomerContact
	var err error
	if req.ContactID != "" {
		contact, err = database.GetCustomerContactByID(req.ContactID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load contact."})
			return
		}
		if contact == nil || contact.CustomerID != customerID {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Contact not found."})
			return
		}
	} else {
		contact, err = database.GetCustomerContactByEmail(customerID, req.ContactEmail)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load contact."})
			return
		}
		if contact == nil {
			contact, err = database.CreateCustomerContact(customerID, "", req.ContactEmail, "", "super_admin")
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create contact for invite."})
				return
			}
		}
	}

	if req.ExpiresInHours <= 0 {
		req.ExpiresInHours = 72
	}
	token := generateRandomToken(48)
	expiresAt := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)

	invite, err := database.CreateOnboardingInvite(customerID, contact.ID, contact.Email, token, expiresAt)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create onboarding invite."})
		return
	}

	_ = database.UpdateCustomerStatus(customerID, "invitation_sent")
	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, invite.ID, actor.ID, actor.Email, "invite_sent", fmt.Sprintf("Sent onboarding invite to %s", contact.Email))

	inviteLink := fmt.Sprintf("%s/onboarding/accept?token=%s", config.AppConfig.FrontendURL, token)
	_ = services.SendOnboardingInviteEmail(contact.Email, contact.FullName, inviteLink)

	_ = json.NewEncoder(w).Encode(struct {
		Success bool                     `json:"success"`
		Message string                   `json:"message"`
		Invite  *models.OnboardingInvite `json:"invite"`
	}{Success: true, Message: "Invite created and sent successfully.", Invite: invite})
}

func getInvite(w http.ResponseWriter, r *http.Request, customerID, inviteID string) {
	invite, err := database.GetOnboardingInviteByID(inviteID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load invite."})
		return
	}
	if invite == nil || invite.CustomerID != customerID {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invite not found."})
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Success bool                     `json:"success"`
		Invite  *models.OnboardingInvite `json:"invite"`
	}{Success: true, Invite: invite})
}

func revokeInvite(w http.ResponseWriter, r *http.Request, customerID, inviteID string) {
	invite, err := database.GetOnboardingInviteByID(inviteID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load invite."})
		return
	}
	if invite == nil || invite.CustomerID != customerID {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invite not found."})
		return
	}

	_, err = database.UpdateInviteStatus(inviteID, "cancelled", time.Time{})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to cancel invite."})
		return
	}

	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customerID, inviteID, actor.ID, actor.Email, "invite_cancelled", fmt.Sprintf("Cancelled invite to %s", invite.ContactEmail))

	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: true, Message: "Invite cancelled successfully."})
}

func listAuditLogs(w http.ResponseWriter, r *http.Request, customerID string) {
	logs, err := database.ListOnboardingAuditLogs(customerID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load audit logs."})
		return
	}
	_ = json.NewEncoder(w).Encode(models.AuditListResponse{Success: true, AuditLogs: logs})
}

func GetOnboardingInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := strings.TrimPrefix(r.URL.Path, "/api/onboarding/invite/")
	token = strings.TrimSpace(token)
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invite token is required."})
		return
	}

	invite, err := database.GetOnboardingInviteByToken(token)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load invite."})
		return
	}
	if invite == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invite not found."})
		return
	}
	if time.Now().After(invite.ExpiresAt) && invite.Status == "pending" {
		_ = json.NewEncoder(w).Encode(struct {
			Success bool                     `json:"success"`
			Invite  *models.OnboardingInvite `json:"invite"`
			Message string                   `json:"message"`
		}{Success: true, Invite: invite, Message: "Invite has expired."})
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Success bool                     `json:"success"`
		Invite  *models.OnboardingInvite `json:"invite"`
	}{Success: true, Invite: invite})
}

func CompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed. Use POST."})
		return
	}

	var req models.CompleteOnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.FullName = strings.TrimSpace(req.FullName)

	if req.Token == "" || req.FullName == "" || len(req.Password) < 6 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Token, full name, and password are required. Password must be at least 6 characters."})
		return
	}

	invite, err := database.GetOnboardingInviteByToken(req.Token)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to load invite."})
		return
	}
	if invite == nil || invite.Status != "sent" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invite token is invalid or no longer active."})
		return
	}
	if time.Now().After(invite.ExpiresAt) {
		_, _ = database.UpdateInviteStatus(invite.ID, "expired", time.Time{})
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invite token has expired."})
		return
	}

	existingUser, err := database.GetUserByEmail(invite.ContactEmail)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Error checking existing user."})
		return
	}
	if existingUser != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "An account already exists for this email. Please sign in."})
		return
	}

	hashedPassword, err := bcryptHash(req.Password)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to hash password."})
		return
	}

	user, err := database.CreateUser(invite.ContactEmail, string(hashedPassword), req.FullName)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create onboarding user account."})
		return
	}

	_, err = database.UpdateInviteStatus(invite.ID, "accepted", time.Now())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to mark invite accepted."})
		return
	}
	_ = database.UpdateCustomerStatus(invite.CustomerID, "completed")
	_ = database.CreateOnboardingAuditLog(invite.CustomerID, invite.ID, "", invite.ContactEmail, "invite_accepted", fmt.Sprintf("Invite accepted by %s", invite.ContactEmail))

	tokenString, err := generateJWT(user.ID, user.Email, 24*time.Hour)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to issue onboarding token."})
		return
	}

	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: true, Message: "Onboarding completed successfully.", Token: tokenString, User: user.ToUserResponse()})
}

func bcryptHash(password string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(password), 10)
}
