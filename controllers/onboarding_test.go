package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/models"
)

func TestCreateCustomer_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/customers", bytes.NewBufferString(`{invalid-json}`))
	rec := httptest.NewRecorder()

	CustomersHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp models.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("unable to decode response: %v", err)
	}
	if resp.Success {
		t.Fatal("expected success=false for invalid JSON payload")
	}
	if resp.Message == "" {
		t.Fatal("expected an error message in response")
	}
}

func TestCreateCustomer_MissingRequiredFields(t *testing.T) {
	payload := map[string]string{
		"name":            "",
		"superAdminName":  "",
		"superAdminEmail": "",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/customers", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()

	CustomersHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp models.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("unable to decode response: %v", err)
	}
	if resp.Success {
		t.Fatal("expected success=false when required fields are missing")
	}
}

func TestGetOnboardingInvite_MissingToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/onboarding/invite/", nil)
	rec := httptest.NewRecorder()

	GetOnboardingInvite(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp models.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("unable to decode response: %v", err)
	}
	if resp.Success {
		t.Fatal("expected success=false for missing invite token")
	}
}

func TestCompleteOnboarding_InvalidPassword(t *testing.T) {
	payload := map[string]string{
		"token":    "fake-token",
		"fullName": "Test User",
		"password": "123",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding/accept", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()

	CompleteOnboarding(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp models.APIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("unable to decode response: %v", err)
	}
	if resp.Success {
		t.Fatal("expected success=false for invalid onboarding payload")
	}
}
