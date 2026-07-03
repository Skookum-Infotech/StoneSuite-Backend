package ai

// NewLLM selects an LLMClient by provider name: "groq" builds a GroqClient,
// anything else (including "" and unrecognized values) defaults to Gemini.
// apiKey/model are the caller-resolved credentials for that provider (kept
// as primitive args, not a config type, so this package stays dependency-free).
func NewLLM(provider, apiKey, model string) LLMClient {
	if provider == "groq" {
		return NewGroqClient(apiKey, model)
	}
	return NewGeminiClient(apiKey, model)
}
