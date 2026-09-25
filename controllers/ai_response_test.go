package controllers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/services"
)

func TestClassifyAIError(t *testing.T) {
	boom := errors.New("boom")

	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantOutcome string
		wantCode    string
	}{
		{
			name:        "plain timeout, no wake ever attempted",
			err:         context.DeadlineExceeded,
			wantStatus:  http.StatusGatewayTimeout,
			wantOutcome: outcomeTimeout,
			wantCode:    codeTimeout,
		},
		{
			name:        "model not found",
			err:         ragcore.ErrModelNotFound,
			wantStatus:  http.StatusServiceUnavailable,
			wantOutcome: outcomeUnavailable,
			wantCode:    codeUnavailable,
		},
		{
			name:        "plain unavailable, no wake ever attempted",
			err:         ragcore.ErrUnavailable,
			wantStatus:  http.StatusServiceUnavailable,
			wantOutcome: outcomeUnavailable,
			wantCode:    codeUnavailable,
		},
		{
			name:        "unrelated error",
			err:         boom,
			wantStatus:  http.StatusBadGateway,
			wantOutcome: outcomeError,
			wantCode:    codeError,
		},
		{
			name:        "wake outcome: waker disabled",
			err:         &wakeOutcome{err: ragcore.ErrUnavailable, wakeErr: services.ErrWakeDisabled},
			wantStatus:  http.StatusForbidden,
			wantOutcome: outcomeUnavailable,
			wantCode:    codeDisabled,
		},
		{
			name:        "wake outcome: cooldown",
			err:         &wakeOutcome{err: ragcore.ErrUnavailable, wakeErr: services.ErrWakeCooldown},
			wantStatus:  http.StatusServiceUnavailable,
			wantOutcome: outcomeUnavailable,
			wantCode:    codeUnavailable,
		},
		{
			name:        "wake outcome: no waker configured",
			err:         &wakeOutcome{err: ragcore.ErrUnavailable},
			wantStatus:  http.StatusServiceUnavailable,
			wantOutcome: outcomeUnavailable,
			wantCode:    codeUnavailable,
		},
		{
			name:        "wake outcome: waked, then the retry itself timed out",
			err:         &wakeOutcome{err: context.DeadlineExceeded, waked: true},
			wantStatus:  http.StatusGatewayTimeout,
			wantOutcome: outcomeTimeout,
			wantCode:    codeStartingUp,
		},
		{
			name:        "wake outcome: waked, retry still unavailable (not a timeout)",
			err:         &wakeOutcome{err: ragcore.ErrUnavailable, waked: true},
			wantStatus:  http.StatusServiceUnavailable,
			wantOutcome: outcomeUnavailable,
			wantCode:    codeUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, outcome, code, msg := classifyAIError(tt.err)
			if status != tt.wantStatus || outcome != tt.wantOutcome || code != tt.wantCode {
				t.Fatalf("classifyAIError() = (%d, %q, %q, %q), want (%d, %q, %q, _)", status, outcome, code, msg, tt.wantStatus, tt.wantOutcome, tt.wantCode)
			}
			if msg == "" {
				t.Fatal("expected a non-empty message")
			}
		})
	}
}
