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

func TestLoadAIConfig(t *testing.T) {
	t.Setenv("AI_LLM_PROVIDER", "gemini")
	t.Setenv("AI_EMBED_PROVIDER", "ollama")
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("AI_CHAT_MODEL", "gemini-1.5-flash")
	t.Setenv("AI_EMBED_MODEL", "nomic-embed-text")
	t.Setenv("AI_EMBED_DIM", "768")
	t.Setenv("OLLAMA_BASE_URL", "http://embedder:11434")
	// required so Load()'s downstream code paths don't panic on empty secrets
	t.Setenv("JWT_SECRET", "x")

	Load()

	if AppConfig.AILLMProvider != "gemini" {
		t.Fatalf("AILLMProvider = %q, want gemini", AppConfig.AILLMProvider)
	}
	if AppConfig.AIEmbedProvider != "ollama" {
		t.Fatalf("AIEmbedProvider = %q, want ollama", AppConfig.AIEmbedProvider)
	}
	if AppConfig.GeminiAPIKey != "test-key" {
		t.Fatalf("GeminiAPIKey = %q, want test-key", AppConfig.GeminiAPIKey)
	}
	if AppConfig.OllamaBaseURL != "http://embedder:11434" {
		t.Fatalf("OllamaBaseURL = %q", AppConfig.OllamaBaseURL)
	}
	if AppConfig.AIEmbedDim != 768 {
		t.Fatalf("AIEmbedDim = %d, want 768", AppConfig.AIEmbedDim)
	}
}

func TestLoadAIConfigDefaults(t *testing.T) {
	t.Setenv("JWT_SECRET", "x")
	Load()
	// ADR-001 defaults: embeddings self-hosted (ollama/nomic), LLM = gemini.
	if AppConfig.AIEmbedProvider != "ollama" {
		t.Fatalf("default AIEmbedProvider = %q, want ollama", AppConfig.AIEmbedProvider)
	}
	if AppConfig.AILLMProvider != "gemini" {
		t.Fatalf("default AILLMProvider = %q, want gemini", AppConfig.AILLMProvider)
	}
	if AppConfig.AIEmbedModel != "nomic-embed-text" {
		t.Fatalf("default AIEmbedModel = %q, want nomic-embed-text", AppConfig.AIEmbedModel)
	}
	if AppConfig.AIEmbedDim != 768 {
		t.Fatalf("default AIEmbedDim = %d, want 768", AppConfig.AIEmbedDim)
	}
}
