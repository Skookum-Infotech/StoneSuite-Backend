package docextractjob

import (
	"math"
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
	// Bound before narrowing to int: a huge env value must not wrap on 32-bit builds.
	maxBytes := c.DocExtractMaxBytes
	if maxBytes > math.MaxInt32 {
		maxBytes = math.MaxInt32
	}
	return Config{
		Limits: docextract.Limits{
			MaxBytes: int(maxBytes),
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
