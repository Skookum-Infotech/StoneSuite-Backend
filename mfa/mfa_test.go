package mfa

import (
	"errors"
	"testing"
	"time"
)

func TestGenerateCode_IsCodeLengthDigits(t *testing.T) {
	const samples = 200
	for i := 0; i < samples; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if !ValidCodeFormat(code) {
			t.Fatalf("GenerateCode() = %q, want %d digits", code, CodeLength)
		}
	}
}

func TestHashCode_RoundTrip(t *testing.T) {
	hash, err := HashCode("042917")
	if err != nil {
		t.Fatalf("HashCode: %v", err)
	}
	if hash == "042917" {
		t.Fatal("HashCode returned the code itself")
	}
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"same code", "042917", true},
		{"one digit off", "042918", false},
		{"leading zero dropped", "42917", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeMatches(hash, tc.code); got != tc.want {
				t.Errorf("CodeMatches(%q) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

func TestNormalizeAndValidateCode(t *testing.T) {
	tests := []struct {
		input     string
		want      string
		wantValid bool
	}{
		{"123456", "123456", true},
		{" 123456 ", "123456", true},
		{"123 456", "123456", true},
		{"123-456", "123456", true},
		{"12345", "12345", false},
		{"1234567", "1234567", false},
		{"12a456", "12a456", false},
		{"１２３４５６", "１２３４５６", false}, // full-width digits are not ASCII digits
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := NormalizeCode(tc.input)
			if got != tc.want {
				t.Errorf("NormalizeCode(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if valid := ValidCodeFormat(got); valid != tc.wantValid {
				t.Errorf("ValidCodeFormat(%q) = %v, want %v", got, valid, tc.wantValid)
			}
		})
	}
}

func TestHashChallengeToken(t *testing.T) {
	a := HashChallengeToken("token-a")
	if a != HashChallengeToken("token-a") {
		t.Error("same token must hash the same")
	}
	if a == HashChallengeToken("token-b") {
		t.Error("different tokens must hash differently")
	}
	const sha256HexLen = 64
	if len(a) != sha256HexLen {
		t.Errorf("len = %d, want %d", len(a), sha256HexLen)
	}
}

func TestMaskEmail(t *testing.T) {
	tests := []struct {
		email string
		want  string
	}{
		{"pat@example.com", "p•••@example.com"},
		{"p@example.com", "p•••@example.com"},
		{"émile@example.fr", "é•••@example.fr"},
		{"no-at-sign", "•••"},
		{"@example.com", "•••"},
		{"", "•••"},
	}
	for _, tc := range tests {
		t.Run(tc.email, func(t *testing.T) {
			if got := MaskEmail(tc.email); got != tc.want {
				t.Errorf("MaskEmail(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}

func TestCheckResend(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		sendCount int
		sentAgo   time.Duration
		wantErr   error
		wantWait  time.Duration
	}{
		{"cooldown passed", 1, ResendCooldown, nil, 0},
		{"just sent", 1, 0, ErrResendTooSoon, ResendCooldown},
		{"half way", 2, ResendCooldown / 2, ErrResendTooSoon, ResendCooldown / 2},
		{"last allowed send", MaxSendsPerChallenge - 1, time.Hour, nil, 0},
		{"send cap reached", MaxSendsPerChallenge, time.Hour, ErrTooManySends, 0},
		{"cap beats cooldown", MaxSendsPerChallenge, 0, ErrTooManySends, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wait, err := CheckResend(tc.sendCount, now.Add(-tc.sentAgo), now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if wait != tc.wantWait {
				t.Errorf("wait = %v, want %v", wait, tc.wantWait)
			}
		})
	}
}

func TestAttemptsLeft(t *testing.T) {
	tests := []struct {
		attempts int
		want     int
	}{
		{0, MaxAttempts},
		{1, MaxAttempts - 1},
		{MaxAttempts, 0},
		{MaxAttempts + 1, 0},
	}
	for _, tc := range tests {
		if got := AttemptsLeft(tc.attempts); got != tc.want {
			t.Errorf("AttemptsLeft(%d) = %d, want %d", tc.attempts, got, tc.want)
		}
	}
}

func TestWaitSeconds(t *testing.T) {
	tests := []struct {
		wait time.Duration
		want int
	}{
		{0, 0},
		{-time.Second, 0},
		{time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{ResendCooldown, int(ResendCooldown / time.Second)},
	}
	for _, tc := range tests {
		if got := WaitSeconds(tc.wait); got != tc.want {
			t.Errorf("WaitSeconds(%v) = %d, want %d", tc.wait, got, tc.want)
		}
	}
}
