package controllers

import (
	"encoding/json"
	"fmt"
	"log"
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
	req.LegalName = strings.TrimSpace(req.LegalName)
	req.Industry = strings.TrimSpace(req.Industry)
	req.Website = strings.TrimSpace(req.Website)
	req.Country = strings.TrimSpace(req.Country)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.Timezone = strings.TrimSpace(req.Timezone)
	req.TaxID = strings.TrimSpace(req.TaxID)
	req.SuperAdminName = strings.TrimSpace(req.SuperAdminName)
	req.SuperAdminEmail = strings.ToLower(strings.TrimSpace(req.SuperAdminEmail))
	req.SuperAdminPhone = strings.TrimSpace(req.SuperAdminPhone)
	req.FinanceName = strings.TrimSpace(req.FinanceName)
	req.FinanceEmail = strings.ToLower(strings.TrimSpace(req.FinanceEmail))

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
	if req.FinanceEmail != "" && !emailRegex.MatchString(req.FinanceEmail) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Finance contact email is not valid."})
		return
	}

	// Length checks — match DB schema column sizes exactly.
	type colLimit struct{ val, field, label string; max int }
	for _, c := range []colLimit{
		{req.Name, "companyName", "Company name", 255},
		{req.LegalName, "legalName", "Legal name", 255},
		{req.Industry, "industry", "Industry", 255},
		{req.Website, "website", "Website URL", 255},
		{req.Country, "country", "Country", 100},
		{req.Currency, "currency", "Currency code", 10},
		{req.Timezone, "timezone", "Timezone", 100},
		{req.TaxID, "taxId", "Tax / VAT ID", 100},
		{req.SuperAdminName, "superAdminName", "Super admin name", 255},
		{req.SuperAdminEmail, "superAdminEmail", "Super admin email", 255},
		{req.SuperAdminPhone, "superAdminPhone", "Super admin phone", 50},
		{req.FinanceName, "financeName", "Finance contact name", 255},
		{req.FinanceEmail, "financeEmail", "Finance contact email", 255},
		{req.FinancePhone, "financePhone", "Finance contact phone", 50},
	} {
		if len(c.val) > c.max {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
				Field   string `json:"field,omitempty"`
			}{Success: false, Message: fmt.Sprintf("%s must be %d characters or fewer (currently %d).", c.label, c.max, len(c.val)), Field: c.field})
			return
		}
	}
	if req.Currency != "" && len(req.Currency) > 3 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Field   string `json:"field,omitempty"`
		}{Success: false, Message: fmt.Sprintf("Currency should be a 3-letter ISO 4217 code (e.g. USD, EUR, GBP). '%s' is too long.", req.Currency), Field: "currency"})
		return
	}

	customer, err := database.CreateCustomer(
		req.Name, req.LegalName, req.Industry, req.Website,
		req.Country, req.Currency, req.Timezone, req.TaxID,
		req.BillingAddress, req.ShippingAddress, req.ReturnAddress,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer record."})
		return
	}

	_, err = database.CreateCustomerContact(customer.ID, req.SuperAdminName, req.SuperAdminEmail, req.SuperAdminPhone, req.SuperAdminJobTitle, "super_admin")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer contact."})
		return
	}

	if req.FinanceEmail != "" {
		_, _ = database.CreateCustomerContact(customer.ID, req.FinanceName, req.FinanceEmail, req.FinancePhone, "", "finance")
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

	updated, err := database.UpdateCustomer(
		customerID, req.Name, req.LegalName, req.Industry, req.Website,
		req.Country, req.Currency, req.Timezone, req.TaxID,
		req.BillingAddress, req.ShippingAddress, req.ReturnAddress, req.Status,
	)
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

	contact, err := database.CreateCustomerContact(customerID, req.FullName, req.Email, req.Phone, "", req.Role)
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

	updated, err := database.UpdateCustomerContact(contactID, req.FullName, req.Email, req.Phone, "", req.Role)
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
			contact, err = database.CreateCustomerContact(customerID, "", req.ContactEmail, "", "", "super_admin")
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create contact for invite."})
				return
			}
		}
	}

	// 2. Prevent conflicting invites and duplicate account creation.
	existingUser, err := database.GetUserByEmail(contact.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Error checking existing user."})
		return
	}
	if existingUser != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "A user account already exists for this email."})
		return
	}

	activeInvite, err := database.GetActiveOnboardingInvite(customerID, contact.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Error checking existing onboarding invites."})
		return
	}
	if activeInvite != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "An active invite already exists for this contact."})
		return
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
	if err := services.SendOnboardingInviteEmail(contact.Email, contact.FullName, inviteLink); err != nil {
		_ = database.DeleteOnboardingInvite(invite.ID)
		log.Printf("ERROR: Invite send failed — email error, rolled back invite %s: %v", invite.ID, err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to send invitation email. No invite was created."})
		return
	}

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

// SendInvitation handles POST /api/invitations — creates a customer shell and sends the invite email.
func SendInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed. Use POST."})
		return
	}

	var req models.SendInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}

	req.CompanyName = strings.TrimSpace(req.CompanyName)
	req.RecipientName = strings.TrimSpace(req.RecipientName)
	req.RecipientEmail = strings.ToLower(strings.TrimSpace(req.RecipientEmail))

	if req.CompanyName == "" || req.RecipientName == "" || req.RecipientEmail == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Company name, recipient name, and email are required."})
		return
	}
	if !emailRegex.MatchString(req.RecipientEmail) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Recipient email is not valid."})
		return
	}

	existingUser, err := database.GetUserByEmail(req.RecipientEmail)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Error checking existing user."})
		return
	}
	if existingUser != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "A user account already exists for this email."})
		return
	}

	customer, err := database.CreateCustomer(req.CompanyName, "", "", "", "", "", "", "", "", "", "")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer record."})
		return
	}

	contact, err := database.CreateCustomerContact(customer.ID, req.RecipientName, req.RecipientEmail, "", "", "super_admin")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create customer contact."})
		return
	}

	if req.ExpiresInHours <= 0 {
		req.ExpiresInHours = 24
	}
	token := generateRandomToken(48)
	expiresAt := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)

	invite, err := database.CreateOnboardingInvite(customer.ID, contact.ID, contact.Email, token, expiresAt)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create onboarding invite."})
		return
	}

	_ = database.UpdateCustomerStatus(customer.ID, "invitation_sent")
	actor, _ := middleware.GetUserFromContext(r.Context())
	_ = database.CreateOnboardingAuditLog(customer.ID, invite.ID, actor.ID, actor.Email, "invite_sent", fmt.Sprintf("Sent onboarding invite to %s (%s)", req.RecipientName, req.RecipientEmail))

	inviteLink := fmt.Sprintf("%s/onboarding/invite/%s", config.AppConfig.FrontendURL, token)
	if err := services.SendOnboardingInviteEmail(contact.Email, contact.FullName, inviteLink); err != nil {
		_ = database.DeleteCustomer(customer.ID) // cascades to contact, invite, audit logs
		log.Printf("ERROR: Invitation failed — email send error, rolled back customer %s: %v", customer.ID, err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to send invitation email. No records were created."})
		return
	}

	_ = json.NewEncoder(w).Encode(struct {
		Success bool                     `json:"success"`
		Message string                   `json:"message"`
		Invite  *models.OnboardingInvite `json:"invite"`
	}{Success: true, Message: "Invitation sent successfully.", Invite: invite})
}

// SubmitOnboarding handles POST /api/onboarding/submit — recipient submits the full customer form.
func SubmitOnboarding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed. Use POST."})
		return
	}

	var req models.SubmitOnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request payload."})
		return
	}

	// Trim all string fields up front.
	req.Token = strings.TrimSpace(req.Token)
	req.Name = strings.TrimSpace(req.Name)
	req.LegalName = strings.TrimSpace(req.LegalName)
	req.Industry = strings.TrimSpace(req.Industry)
	req.Website = strings.TrimSpace(req.Website)
	req.Country = strings.TrimSpace(req.Country)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.Timezone = strings.TrimSpace(req.Timezone)
	req.TaxID = strings.TrimSpace(req.TaxID)
	req.BillingAddress = strings.TrimSpace(req.BillingAddress)
	req.ShippingAddress = strings.TrimSpace(req.ShippingAddress)
	req.ReturnAddress = strings.TrimSpace(req.ReturnAddress)
	req.SuperAdminName = strings.TrimSpace(req.SuperAdminName)
	req.SuperAdminEmail = strings.ToLower(strings.TrimSpace(req.SuperAdminEmail))
	req.SuperAdminPhone = strings.TrimSpace(req.SuperAdminPhone)
	req.SuperAdminJobTitle = strings.TrimSpace(req.SuperAdminJobTitle)
	req.FinanceName = strings.TrimSpace(req.FinanceName)
	req.FinanceEmail = strings.ToLower(strings.TrimSpace(req.FinanceEmail))
	req.FinancePhone = strings.TrimSpace(req.FinancePhone)

	// --- Field validation (matches DB schema constraints) ---
	type fieldErr struct{ field, msg string }
	var errs []fieldErr

	// Required fields
	if req.Name == "" {
		errs = append(errs, fieldErr{"companyName", "Company name is required."})
	}
	if req.SuperAdminName == "" {
		errs = append(errs, fieldErr{"superAdminName", "Super admin full name is required."})
	}
	if req.SuperAdminEmail == "" {
		errs = append(errs, fieldErr{"superAdminEmail", "Super admin email is required."})
	}

	// Length limits — exact column definitions from the DB schema
	checkLen := func(val, field string, max int, label string) {
		if len(val) > max {
			errs = append(errs, fieldErr{field, fmt.Sprintf("%s must be %d characters or fewer (currently %d).", label, max, len(val))})
		}
	}
	checkLen(req.Name, "companyName", 255, "Company name")
	checkLen(req.LegalName, "legalName", 255, "Legal name")
	checkLen(req.Industry, "industry", 255, "Industry")
	checkLen(req.Website, "website", 255, "Website URL")
	checkLen(req.Country, "country", 100, "Country")
	checkLen(req.Currency, "currency", 10, "Currency code")
	checkLen(req.Timezone, "timezone", 100, "Timezone")
	checkLen(req.TaxID, "taxId", 100, "Tax / VAT ID")
	checkLen(req.SuperAdminName, "superAdminName", 255, "Super admin name")
	checkLen(req.SuperAdminEmail, "superAdminEmail", 255, "Super admin email")
	checkLen(req.SuperAdminPhone, "superAdminPhone", 50, "Super admin phone")
	checkLen(req.SuperAdminJobTitle, "superAdminJobTitle", 255, "Super admin job title")
	checkLen(req.FinanceName, "financeName", 255, "Finance contact name")
	checkLen(req.FinanceEmail, "financeEmail", 255, "Finance contact email")
	checkLen(req.FinancePhone, "financePhone", 50, "Finance contact phone")

	// Email format checks
	if req.SuperAdminEmail != "" && !emailRegex.MatchString(req.SuperAdminEmail) {
		errs = append(errs, fieldErr{"superAdminEmail", "Super admin email address is not valid."})
	}
	if req.FinanceEmail != "" && !emailRegex.MatchString(req.FinanceEmail) {
		errs = append(errs, fieldErr{"financeEmail", "Finance contact email address is not valid."})
	}

	// Currency: should be a 2–3 letter ISO 4217 code
	if req.Currency != "" && len(req.Currency) > 3 {
		errs = append(errs, fieldErr{"currency", fmt.Sprintf("Currency should be a 3-letter ISO 4217 code (e.g. USD, EUR, GBP). '%s' is too long.", req.Currency)})
	}

	if len(errs) > 0 {
		// Return the first (most important) validation error with its field label so the UI can surface it.
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Field   string `json:"field,omitempty"`
		}{Success: false, Message: errs[0].msg, Field: errs[0].field})
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

	_, err = database.UpdateCustomer(
		invite.CustomerID, req.Name, req.LegalName, req.Industry, req.Website,
		req.Country, req.Currency, req.Timezone, req.TaxID,
		req.BillingAddress, req.ShippingAddress, req.ReturnAddress, "pendingApproval",
	)
	if err != nil {
		log.Printf("ERROR: SubmitOnboarding UpdateCustomer failed for customer %s: %v", invite.CustomerID, err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to save your information. Please check your inputs and try again."})
		return
	}

	if invite.ContactID != "" {
		_, _ = database.UpdateCustomerContact(invite.ContactID, req.SuperAdminName, req.SuperAdminEmail, req.SuperAdminPhone, req.SuperAdminJobTitle, "super_admin")
	}

	if req.FinanceEmail != "" {
		contacts, _ := database.GetCustomerContacts(invite.CustomerID)
		var financeContact *models.CustomerContact
		for i := range contacts {
			if contacts[i].Role == "finance" {
				financeContact = &contacts[i]
				break
			}
		}
		if financeContact != nil {
			_, _ = database.UpdateCustomerContact(financeContact.ID, req.FinanceName, req.FinanceEmail, req.FinancePhone, "", "finance")
		} else {
			_, _ = database.CreateCustomerContact(invite.CustomerID, req.FinanceName, req.FinanceEmail, req.FinancePhone, "", "finance")
		}
	}

	_, _ = database.UpdateInviteStatus(invite.ID, "accepted", time.Now())
	_ = database.CreateOnboardingAuditLog(invite.CustomerID, invite.ID, "", invite.ContactEmail, "onboarding_submitted", fmt.Sprintf("Onboarding form submitted by %s", invite.ContactEmail))

	customer, err := database.GetCustomerByIDWithContacts(invite.CustomerID)
	if err != nil || customer == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to retrieve updated customer."})
		return
	}

	_ = json.NewEncoder(w).Encode(struct {
		Success  bool             `json:"success"`
		Message  string           `json:"message"`
		Customer *models.Customer `json:"customer"`
	}{Success: true, Message: "Onboarding submitted successfully.", Customer: customer})
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
	if time.Now().After(invite.ExpiresAt) && invite.Status == "sent" {
		_, _ = database.UpdateInviteStatus(invite.ID, "expired", time.Time{})
		invite.Status = "expired"
	}

	var companyName, recipientName string
	if customer, cerr := database.GetCustomerByIDWithContacts(invite.CustomerID); cerr == nil && customer != nil {
		companyName = customer.Name
		for _, c := range customer.Contacts {
			if c.Email == invite.ContactEmail {
				recipientName = c.FullName
				break
			}
		}
	}

	_ = json.NewEncoder(w).Encode(struct {
		Success       bool                     `json:"success"`
		Invite        *models.OnboardingInvite `json:"invite"`
		CompanyName   string                   `json:"companyName"`
		RecipientName string                   `json:"recipientName"`
	}{Success: true, Invite: invite, CompanyName: companyName, RecipientName: recipientName})
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
