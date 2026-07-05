package ai

import "testing"

func TestNewLLM_SelectsByProvider(t *testing.T) {
	tests := []struct {
		provider string
		wantType string
	}{
		{"gemini", "*ai.GeminiClient"},
		{"groq", "*ai.GroqClient"},
		{"ollama", "*ai.OllamaLLMClient"},
		{"", "*ai.GeminiClient"},      // default
		{"bogus", "*ai.GeminiClient"}, // unknown falls back to default
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			llm := NewLLM(tt.provider, "key", "model", "http://ollama:11434")
			var got string
			switch llm.(type) {
			case *GeminiClient:
				got = "*ai.GeminiClient"
			case *GroqClient:
				got = "*ai.GroqClient"
			case *OllamaLLMClient:
				got = "*ai.OllamaLLMClient"
			default:
				got = "unknown"
			}
			if got != tt.wantType {
				t.Fatalf("provider %q: got %s, want %s", tt.provider, got, tt.wantType)
			}
		})
	}
}
