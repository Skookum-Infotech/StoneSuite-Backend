package docextractjob

import (
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/docextract"
)

// Config is the worker's tuning, injected so the package never reads the
// global config.AppConfig.
type Config struct {
	Limits      docextract.Limits
	TokenBudget int
	LLMTimeout  time.Duration
	StagingTTL  time.Duration
	// Model is the chat model tag recorded on an extraction that used the LLM fallback.
	Model string
}

// ConfigFromApp builds a Config from the DocExtract* app settings.
func ConfigFromApp(c config.Config) Config {
	return Config{
		Limits: docextract.Limits{
			MaxBytes: int(c.DocExtractMaxBytes),
			MaxPages: c.DocExtractMaxPages,
			MaxLines: c.DocExtractMaxLines,
			MaxQty:   int64(c.DocExtractMaxQty),
		},
		TokenBudget: c.DocExtractLLMTokenBudget,
		LLMTimeout:  c.DocExtractLLMTimeout,
		StagingTTL:  c.DocExtractStagingTTL,
		Model:       c.AIChatModel,
	}
}
