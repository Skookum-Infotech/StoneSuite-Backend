package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/services"
)

func TestApplyEmailOutcome(t *testing.T) {
	const providerDetail = "resend: unexpected status 403: domain not verified"

	tests := []struct {
		name          string
		outcome       services.EmailOutcome
		wantSent      any // nil => key must be absent
		wantErrorText string
	}{
		{"nothing attempted adds no fields", services.EmailOutcome{}, nil, ""},
		{"sent", services.EmailOutcome{Status: services.EmailStatusSent}, true, ""},
		{"failed", services.EmailOutcome{Status: services.EmailStatusFailed, Detail: providerDetail}, false,
			services.EmailOutcome{Status: services.EmailStatusFailed}.UserMessage()},
		{"pending", services.EmailOutcome{Status: services.EmailStatusPending}, false,
			services.EmailOutcome{Status: services.EmailStatusPending}.UserMessage()},
		{"skipped", services.EmailOutcome{Status: services.EmailStatusSkipped}, false,
			services.EmailOutcome{Status: services.EmailStatusSkipped}.UserMessage()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := map[string]any{"success": true}

			applyEmailOutcome(context.Background(), resp, tt.outcome, "email not delivered", "record", "r1")

			if tt.wantSent == nil {
				assert.NotContains(t, resp, "emailSent")
				assert.NotContains(t, resp, "emailError")
				return
			}
			assert.Equal(t, tt.wantSent, resp["emailSent"])
			if tt.wantErrorText == "" {
				assert.NotContains(t, resp, "emailError")
				return
			}
			assert.Equal(t, tt.wantErrorText, resp["emailError"])
			assert.NotContains(t, resp["emailError"], providerDetail, "provider detail must stay in the log")
		})
	}
}
