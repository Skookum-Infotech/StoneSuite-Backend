package ai

// NewLLM selects an LLMClient by provider name: "groq" builds a GroqClient,
// "ollama" builds an OllamaLLMClient (self-hosted, no apiKey needed —
// ollamaBaseURL is used instead), anything else (including "" and
// unrecognized values) defaults to Gemini. apiKey/model/ollamaBaseURL are the
// caller-resolved credentials for that provider (kept as primitive args, not
// a config type, so this package stays dependency-free).
func NewLLM(provider, apiKey, model, ollamaBaseURL string) LLMClient {
	switch provider {
	case "groq":
		return NewGroqClient(apiKey, model)
	case "ollama":
		return NewOllamaLLMClient(ollamaBaseURL, model)
	default:
		return NewGeminiClient(apiKey, model)
	}
}
