package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/database"
	"stonesuite-backend/models"
)

func LeadsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		listLeads(w, r)
	case http.MethodPost:
		createLead(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed. Use GET or POST."})
	}
}

func listLeads(w http.ResponseWriter, _ *http.Request) {
	leads, err := database.GetAllLeads()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to retrieve leads: " + err.Error()})
		return
	}
	if leads == nil {
		leads = []models.Lead{}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"leads":   leads,
	})
}

func createLead(w http.ResponseWriter, r *http.Request) {
	var req models.CreateLeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Invalid request body."})
		return
	}

	if req.Type == "" {
		req.Type = "Company"
	}
	if req.LeadStatus == "" {
		req.LeadStatus = "LEAD-Unqualified"
	}
	if req.CustomForm == "" {
		req.CustomForm = "Standard Lead Form"
	}

	lead, err := database.CreateLead(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Failed to create lead: " + err.Error()})
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Lead created successfully.",
		"lead":    lead,
	})
}
