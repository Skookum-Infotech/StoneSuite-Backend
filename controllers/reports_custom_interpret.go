package controllers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
)

// maxInterpretPromptBytes bounds the plain-English request, matching the
// assistant's own question limit's order of magnitude and the UI's counter.
const maxInterpretPromptBytes = 2000

// codeUninterpretable is the machine-readable reason for a reply the model
// could not turn into a report (see classifyAIError for the AI codes).
const codeUninterpretable = "uninterpretable"

// Output budgets for the two model calls: the source choice is one short key;
// a definition can carry a few filters and totals. Bounded because every
// unused token is generation time on a CPU-only box.
const (
	sourceChoiceMaxTokens = 60
	definitionMaxTokens   = 450
)

const msgInterpretUnavailable = "The AI assistant is not available right now. You can still build the report by hand."

// modelCall is one schema-constrained model request. It is a seam so the
// two-step flow is testable without a model.
type modelCall func(ctx context.Context, system, user string, schema []byte, maxTokens int) (string, error)

// validateInterpretPrompt trims the request and enforces its size in bytes.
func validateInterpretPrompt(raw string) (string, error) {
	prompt := strings.TrimSpace(raw)
	if prompt == "" {
		return "", &reports.ValidationError{Field: "prompt", Message: "Describe the report you want."}
	}
	if len(prompt) > maxInterpretPromptBytes {
		return "", &reports.ValidationError{Field: "prompt", Message: "That request is too long, please shorten it."}
	}
	return prompt, nil
}

// runInterpretation turns a plain-English request into a repaired, validated
// report definition using two small model calls: first which source fits, then
// that one source's definition. A request that edits a current report skips the
// first call (the source stays). A model error is returned as is so the
// handler can classify it; a reply that cannot be used is ErrUninterpretable.
func runInterpretation(ctx context.Context, call modelCall, sources []reports.Source, prompt string, current *reports.Definition, today time.Time) (reports.Interpretation, error) {
	var src reports.Source
	if current != nil {
		found := false
		for _, s := range sources {
			if s.Key == current.Source {
				src, found = s, true
			}
		}
		if !found {
			return reports.Interpretation{}, reports.ErrUninterpretable
		}
	} else {
		raw, err := call(ctx, reports.SourceChoicePrompt(sources), prompt, reports.SourceChoiceSchema(sources), sourceChoiceMaxTokens)
		if err != nil {
			return reports.Interpretation{}, err
		}
		if src, err = reports.ParseSourceChoice(raw, sources); err != nil {
			return reports.Interpretation{}, err
		}
	}
	raw, err := call(ctx, reports.DefinitionPrompt(src, today, current), prompt, reports.DefinitionSchema(src), definitionMaxTokens)
	if err != nil {
		return reports.Interpretation{}, err
	}
	return reports.ParseInterpretation(raw, src, current, today)
}

// interpretErrorStatus maps a failed interpretation to the status, machine code
// and message the client sees. Model and wake failures reuse the assistant's own
// classification so every AI endpoint speaks the same codes.
func interpretErrorStatus(err error) (status int, code, message string) {
	if errors.Is(err, reports.ErrUninterpretable) {
		return http.StatusUnprocessableEntity, codeUninterpretable,
			"I couldn't turn that into a report. Try naming what to count or total, e.g. 'invoice revenue by customer this quarter'."
	}
	status, _, code, message = classifyAIError(err)
	if status == http.StatusServiceUnavailable || status == http.StatusBadGateway {
		message = msgInterpretUnavailable
	}
	return status, code, message
}

// tokenBounded is an LLM client that can cap one call's output tokens
// (provider/ollama.LLMClient.WithMaxTokens).
type tokenBounded interface {
	WithMaxTokens(n int) ragcore.LLMClient
}

// structuredCall wraps the assistant's model as a modelCall: schema-constrained
// output, a per-call token cap when the client supports one, and the same
// on-demand Ollama wake the assistant uses. ok is false when the configured
// model cannot do structured output.
func (h *AIOps) structuredCall() (modelCall, bool) {
	if _, ok := h.llm.(ragcore.StructuredLLMClient); !ok {
		return nil, false
	}
	return func(ctx context.Context, system, user string, schema []byte, maxTokens int) (string, error) {
		client := h.llm
		if bounded, ok := client.(tokenBounded); ok {
			client = bounded.WithMaxTokens(maxTokens)
		}
		structured, ok := client.(ragcore.StructuredLLMClient)
		if !ok {
			return "", ragcore.ErrUnavailable
		}
		res, err := h.withOllamaWake(ctx, nil, func(ragcore.StreamSink) (ragcore.AskResult, error) {
			raw, err := structured.ChatJSON(ctx, system, []ragcore.Message{{Role: "user", Content: user}}, schema)
			return ragcore.AskResult{Answer: raw}, err
		})
		return res.Answer, err
	}, true
}

type interpretBody struct {
	Prompt  string              `json:"prompt"`
	Current *reports.Definition `json:"current"`
}

// InterpretCustomReport turns a plain-English request into a report definition
// for the builder to show and the user to check. It never runs the report and
// the model never writes SQL: see reports.ParseInterpretation.
// POST /api/tenant/reports/custom/interpret  body {prompt, current?}
func (h *AIOps) InterpretCustomReport(w http.ResponseWriter, r *http.Request) {
	caller, ok := authorizeCustomCaller(w, r, authz.ActionRead)
	if !ok {
		return
	}
	aiStatus, err := h.aiSettings.Status(r.Context(), h.cpPool, caller.pool, caller.tenantID)
	if err != nil {
		slog.Error("custom report interpret: ai settings check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !aiStatus.Available {
		writeAssistantDisabled(w, aiStatus)
		return
	}
	var body interpretBody
	if !decodeCustomBody(w, r, &body) {
		return
	}
	prompt, err := validateInterpretPrompt(body.Prompt)
	if err != nil {
		writeCustomError(w, r, err)
		return
	}
	sources := caller.access.sources()
	if len(sources) == 0 {
		fail(w, http.StatusForbidden, "You do not have access to any report sources.")
		return
	}
	call, ok := h.structuredCall()
	if !ok {
		failWithCode(w, http.StatusServiceUnavailable, msgInterpretUnavailable, codeUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), askMaxDuration)
	defer cancel()
	release, ok := h.acquireSlot(ctx, caller.tenantID)
	if !ok {
		writeAssistantBusy(w)
		return
	}
	defer release()

	result, err := runInterpretation(ctx, call, sources, prompt, body.Current, time.Now())
	if err != nil {
		httpStatus, code, msg := interpretErrorStatus(err)
		if !errors.Is(err, reports.ErrUninterpretable) {
			slog.Error("custom report interpret failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", caller.tenantID, "err", err)
		}
		if httpStatus == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", strconv.Itoa(aiBusyRetryAfter))
		}
		failWithCode(w, httpStatus, msg, code)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "interpretation": result})
}
