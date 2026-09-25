package controllers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/metrics"
	"stonesuite-backend/models"
	"stonesuite-backend/services"
)

// Error codes a client can branch on, carried on every AI error response —
// SSE "error" event and non-streaming JSON alike (see classifyAIError).
const (
	codeStartingUp  = "starting_up"
	codeTimeout     = "timeout"
	codeUnavailable = "unavailable"
	codeDisabled    = "disabled"
	codeError       = "error"
)

// citationDTO is a citation as the API sends it: the library's public fields
// plus record_type, which the client needs to build a link to the record
// (a record id alone doesn't say which of lead/prospect/customer it is).
type citationDTO struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id,omitempty"`
	Snippet    string `json:"snippet"`
	RecordType string `json:"record_type,omitempty"`
}

// citationDTOs converts cites for the API, looking up each record citation's
// record_type in one query. Best-effort: on a lookup failure the citations
// still go out, just without a type (the client then renders them unlinked).
func citationDTOs(ctx context.Context, pool *pgxpool.Pool, cites []ragcore.Citation) []citationDTO {
	out := make([]citationDTO, 0, len(cites))
	var ids []string
	for _, c := range cites {
		out = append(out, citationDTO{SourceType: c.SourceType, SourceID: c.SourceID, Snippet: c.Snippet})
		if c.SourceType == ai.CorpusRecords && c.SourceID != "" {
			ids = append(ids, c.SourceID)
		}
	}
	if len(ids) == 0 || pool == nil {
		return out
	}
	rows, err := pool.Query(ctx, `SELECT source_id::text, COALESCE(record_type, '') FROM rag_chunks WHERE source_id = ANY($1::uuid[])`, ids)
	if err != nil {
		slog.Warn("ai citation record_type lookup failed", "err", err)
		return out
	}
	defer rows.Close()
	types := map[string]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			slog.Warn("ai citation record_type scan failed", "err", err)
			return out
		}
		types[id] = t
	}
	for i := range out {
		if out[i].SourceType == ai.CorpusRecords {
			out[i].RecordType = types[out[i].SourceID]
		}
	}
	return out
}

// classifyAIError maps a dispatch failure to what the client is told. The
// distinctions matter to a user: "starting up, retry in a moment", "the
// assistant is switched off", and "took too long, try again" call for
// different actions than a generic failure.
//
// A *wakeOutcome (see withOllamaWake) is unwrapped first so the message
// reflects what actually happened to the on-demand Ollama start, not just the
// generic ErrUnavailable/timeout underneath it.
func classifyAIError(err error) (status int, outcome, code, message string) {
	var wo *wakeOutcome
	if errors.As(err, &wo) {
		switch {
		case errors.Is(wo.wakeErr, services.ErrWakeDisabled):
			return http.StatusForbidden, outcomeUnavailable, codeDisabled, "The StoneSuite Assistant is turned off."
		case wo.wakeErr != nil:
			// services.ErrWakeCooldown or any other OllamaWaker failure.
			return http.StatusServiceUnavailable, outcomeUnavailable, codeUnavailable, "The assistant is unavailable right now. Please try again in a minute."
		case !wo.waked:
			// No waker configured at all — nothing this server can do to
			// bring it up on demand.
			return http.StatusServiceUnavailable, outcomeUnavailable, codeUnavailable, "The assistant is unavailable right now. Please try again in a minute."
		case errors.Is(wo.err, context.DeadlineExceeded):
			return http.StatusGatewayTimeout, outcomeTimeout, codeStartingUp, "The assistant was starting up and took too long — please try again now."
		}
		err = wo.err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, outcomeTimeout, codeTimeout, "The assistant took too long to answer. Please try again, or ask a shorter question."
	case errors.Is(err, ragcore.ErrModelNotFound):
		return http.StatusServiceUnavailable, outcomeUnavailable, codeUnavailable, "The assistant isn't available on this server yet. Please contact your administrator."
	case errors.Is(err, ragcore.ErrUnavailable):
		return http.StatusServiceUnavailable, outcomeUnavailable, codeUnavailable, "The assistant is starting up — please try again in a few seconds."
	default:
		return http.StatusBadGateway, outcomeError, codeError, "The assistant is temporarily unavailable. Please try again."
	}
}

// failWithCode writes a JSON error response carrying a machine-readable code
// (see classifyAIError) alongside the human message — same shape as
// middleware.RateLimiter's coded 429, so a client branches on codes
// consistently across the AI endpoints and the rate limiter.
func failWithCode(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, models.APIResponse{Success: false, Message: msg, Code: code})
}

// finishAsk records the per-request metrics and the ai_query security log
// line shared by both endpoints.
func finishAsk(r *http.Request, pa preparedAsk, endpoint, route, outcome string, start time.Time) {
	metrics.ObserveAIRequest(endpoint, route, outcome, time.Since(start).Seconds())
	logSecurityEvent(r, "ai_query", "tenant_id", pa.tenant.ID, "user_id", pa.callerUserID,
		"endpoint", endpoint, "route", route, "outcome", outcome, "mode", pa.mode())
}

// observeSuccess records metrics only a successful answer has.
func observeSuccess(route string, res ragcore.AskResult) {
	metrics.ObserveAIQueryRoute(route)
	if res.Grounded {
		metrics.ObserveAIUsage(res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.Truncated)
	}
}
