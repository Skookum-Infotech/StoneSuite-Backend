package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/middleware"
)

// sseEvent is one SSE frame queued from the background dispatch goroutine to
// the AskStream handler goroutine, which is the only one allowed to write to
// the ResponseWriter (see channelSink).
type sseEvent struct {
	event string
	data  any
}

// channelSink adapts rag.StreamSink to a channel instead of writing directly:
// AskStream's dispatch runs on a background goroutine so the handler's own
// goroutine can interleave heartbeats with real events, and
// http.ResponseWriter is not safe for concurrent writes — so exactly one
// goroutine (the handler's select loop) ever touches it. Every send respects
// ctx: once the handler gives up (client gone, absolute timeout), the sink
// returns ctx.Err() instead of blocking against a channel nobody reads, which
// lets the dispatch (and ChatStream beneath it) unwind without leaking.
type channelSink struct {
	ch   chan<- sseEvent
	ctx  context.Context
	pool *pgxpool.Pool
}

func (s *channelSink) send(ev sseEvent) error {
	select {
	case s.ch <- ev:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *channelSink) OnRetrieved(cites []ragcore.Citation) error {
	return s.send(sseEvent{event: "sources", data: map[string]any{"citations": citationDTOs(s.ctx, s.pool, cites)}})
}

func (s *channelSink) OnToken(token string) error {
	return s.send(sseEvent{event: "token", data: token})
}

// writeSSE marshals data as JSON and writes one SSE frame. data must marshal
// to a single line (json.Marshal never emits raw newlines), which is what
// makes the two-line "event:"/"data:" framing valid per the SSE spec.
func writeSSE(w io.Writer, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal sse data: %w", err)
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}

// streamMaxDuration is the absolute ceiling on one SSE stream regardless of
// how much progress it's making — the safety net against a wedged connection
// pinning a goroutine (and a model slot) forever.
const streamMaxDuration = 5 * time.Minute

// streamWriteDeadline is re-armed (via http.ResponseController) on every SSE
// event, including heartbeats: how long the connection may go silent before
// it's considered dead. Requires statusRecorder.Unwrap (middleware/logging.go)
// to reach the real ResponseWriter through the global middleware chain.
const streamWriteDeadline = 30 * time.Second

// streamHeartbeatInterval is how often a ": ping" comment line is sent during
// any gap between real events — short enough that neither
// streamWriteDeadline nor Cloudflare's edge buffering ever sees true silence,
// including during prefill before the first token.
const streamHeartbeatInterval = 15 * time.Second

// AskStream handles POST /api/tenant/ai/ask/stream — the SSE twin of Ask.
// Same chain and the identical security checks via prepareAsk, the same model
// slot limiter, and the same dispatch; the difference is delivery.
//
// Event sequence: "sources" once (the retrieved set, before generation starts
// — render as "found N sources", never as citations: filtering to what the
// answer actually references only happens in "done") -> "token"* as generated
// -> exactly one of "done" or "error". A ": ping" comment line may appear
// between any of these.
//
// The conversation turn is persisted only after "done": a client that
// disconnects mid-stream leaves nothing in history, deliberately.
func (h *AIOps) AskStream(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, pa, ok := h.prepareAsk(w, r)
	if !ok {
		return
	}

	// Slot acquisition happens before any SSE header so a busy 429 is still an
	// ordinary JSON response.
	if needsModel(body.Question) {
		release, ok := h.acquireSlot(r.Context(), pa.tenant.ID)
		if !ok {
			finishAsk(r, pa, endpointStream, routeRAG, outcomeBusy, start)
			writeAssistantBusy(w)
			return
		}
		defer release()
	}

	// Everything above can still fail with an ordinary JSON 4xx/5xx. Nothing
	// below can: once the 200 + SSE headers are written, the only way to
	// signal failure is an "error" event.
	ctx, cancel := context.WithTimeout(r.Context(), streamMaxDuration)
	defer cancel()

	// http.ResponseController, not a w.(http.Flusher) type assertion, is the
	// only mechanism that reaches Flush/SetWriteDeadline through the global
	// RequestLogger(Recover(...)) chain: RequestLogger hands every handler a
	// *statusRecorder that embeds http.ResponseWriter — promoting only
	// Header/Write/WriteHeader — so a type assertion against w always fails
	// even though the connection supports flushing. ResponseController walks
	// each wrapper's Unwrap() instead.
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	armDeadline := func() { _ = rc.SetWriteDeadline(time.Now().Add(streamWriteDeadline)) }
	armDeadline()
	if err := rc.Flush(); err != nil {
		slog.Error("ai ask stream: initial flush unsupported", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "err", err)
		return
	}

	store := crmstore.For(pa.tenant.DesignVersion)
	events := make(chan sseEvent, 4)
	sink := &channelSink{ch: events, ctx: ctx, pool: pa.pool}

	type outcome struct {
		res   ragcore.AskResult
		route string
		err   error
	}
	// finalOutcome is written exactly once, by the background goroutine,
	// strictly before it closes events, and never read until the select loop
	// below observes events closed. Closing a channel happens-before a receive
	// that returns because it's closed, so the write is always visible when
	// read. (A separate buffered outcome channel raced: select could pick it
	// before events drained, delivering "done" ahead of buffered tokens.)
	var finalOutcome outcome
	go func() {
		res, route, err := runAskDispatch(ctx, h, pa, store, body.Question, sink)
		finalOutcome = outcome{res: res, route: route, err: err}
		close(events)
	}()

	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case ev, open := <-events:
			if !open {
				h.finishStream(w, rc, r, pa, body.Question, finalOutcome.res, finalOutcome.route, finalOutcome.err, start)
				return
			}
			if err := writeSSE(w, ev.event, ev.data); err != nil {
				// The client is gone. Returning triggers the deferred cancel(),
				// which unwinds the background goroutine.
				finishAsk(r, pa, endpointStream, routeRAG, outcomeDisconnected, start)
				return
			}
			armDeadline()
			_ = rc.Flush()

		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				finishAsk(r, pa, endpointStream, routeRAG, outcomeDisconnected, start)
				return
			}
			armDeadline()
			_ = rc.Flush()

		case <-ctx.Done():
			// Client disconnected (r.Context() cancelled) or a deadline hit.
			// Nothing is persisted either way; only a deadline still has a
			// client listening for the error event.
			if errors.Is(r.Context().Err(), context.Canceled) {
				finishAsk(r, pa, endpointStream, routeRAG, outcomeDisconnected, start)
				return
			}
			finishAsk(r, pa, endpointStream, routeRAG, outcomeTimeout, start)
			_ = writeSSE(w, "error", map[string]any{"success": false, "message": "The assistant took too long to respond."})
			_ = rc.Flush()
			return
		}
	}
}

// finishStream writes the terminal "done" or "error" event once dispatch has
// returned.
func (h *AIOps) finishStream(w io.Writer, rc *http.ResponseController, r *http.Request, pa preparedAsk, question string, res ragcore.AskResult, route string, err error, start time.Time) {
	if err != nil {
		_, outcome, msg := classifyAIError(err)
		slog.Error("ai ask stream failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", pa.tenant.ID, "route", route, "err", err)
		finishAsk(r, pa, endpointStream, route, outcome, start)
		_ = writeSSE(w, "error", map[string]any{"success": false, "message": msg})
		_ = rc.Flush()
		return
	}
	observeSuccess(route, res)
	persisted := recordTurn(r, pa, question, res.Answer)
	finishAsk(r, pa, endpointStream, route, outcomeOK, start)
	data := map[string]any{
		"answer":    res.Answer,
		"citations": citationDTOs(r.Context(), pa.pool, res.Citations),
		"truncated": res.Usage.Truncated,
	}
	if pa.conv != nil {
		data["conversation_id"] = pa.conv.ID
		data["persisted"] = persisted
	}
	_ = writeSSE(w, "done", data)
	_ = rc.Flush()
}
