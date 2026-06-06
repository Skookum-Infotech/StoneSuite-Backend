package config

import "testing"

// A valid base64-encoded 32-byte AES key (openssl rand -base64 32 shape).
const valid32ByteKeyB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEA="

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "dev: jwt set, no encryption key is allowed",
			cfg:     Config{Environment: "development", JWTSecret: "x"},
			wantErr: false,
		},
		{
			name:    "missing jwt secret is rejected",
			cfg:     Config{Environment: "development", JWTSecret: ""},
			wantErr: true,
		},
		{
			name:    "production without encryption key is rejected",
			cfg:     Config{Environment: "production", JWTSecret: "x"},
			wantErr: true,
		},
		{
			name:    "production with valid 32-byte key is allowed",
			cfg:     Config{Environment: "production", JWTSecret: "x", SecretEncryptionKey: valid32ByteKeyB64},
			wantErr: false,
		},
		{
			name:    "non-base64 encryption key is rejected",
			cfg:     Config{Environment: "development", JWTSecret: "x", SecretEncryptionKey: "not base64!!!"},
			wantErr: true,
		},
		{
			name:    "wrong-length encryption key is rejected",
			cfg:     Config{Environment: "development", JWTSecret: "x", SecretEncryptionKey: "YWJj"}, // "abc" -> 3 bytes
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

func TestIsProduction(t *testing.T) {
	if !(Config{Environment: "production"}).IsProduction() {
		t.Error("expected production")
	}
	if !(Config{Environment: "Production"}).IsProduction() {
		t.Error("expected case-insensitive production match")
	}
	if (Config{Environment: "development"}).IsProduction() {
		t.Error("development must not be production")
	}
}
