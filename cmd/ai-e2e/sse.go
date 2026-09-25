package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// sseFrame is one parsed "event: X\ndata: Y\n\n" frame.
type sseFrame struct {
	Event string
	Data  string
}

// sseAccumulator folds SSE lines into frames one at a time, so a live
// network reader and the pure test-facing parser below share identical
// framing logic. ": ping" comment lines and blank frames (no event/data seen
// yet) never emit.
type sseAccumulator struct {
	event     string
	dataLines []string
}

// feed consumes one line (no trailing newline) and reports the frame it
// completed, if any. A blank line is the SSE frame terminator.
func (a *sseAccumulator) feed(line string) (sseFrame, bool) {
	switch {
	case line == "":
		if a.event == "" && len(a.dataLines) == 0 {
			return sseFrame{}, false
		}
		f := sseFrame{Event: a.event, Data: strings.Join(a.dataLines, "\n")}
		a.event = ""
		a.dataLines = nil
		return f, true
	case strings.HasPrefix(line, ":"):
		// Comment line, e.g. ": ping" heartbeats — never part of a frame.
		return sseFrame{}, false
	case strings.HasPrefix(line, "event:"):
		a.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return sseFrame{}, false
	case strings.HasPrefix(line, "data:"):
		a.dataLines = append(a.dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		return sseFrame{}, false
	default:
		return sseFrame{}, false
	}
}

// parseSSEFrames parses a complete SSE body into its frames. Pure and used
// directly by tests; the live streaming client below runs the same
// sseAccumulator line-by-line as bytes arrive instead of on a complete body,
// so ttft/total can be timed per frame.
func parseSSEFrames(raw string) []sseFrame {
	var frames []sseFrame
	acc := &sseAccumulator{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if f, ok := acc.feed(line); ok {
			frames = append(frames, f)
		}
	}
	return frames
}

// decodeTokenData decodes a "token" frame's data, which the server always
// marshals as a JSON string (writeSSE(w, "token", token) — json.Marshal of a
// Go string), never a raw literal.
func decodeTokenData(data string) (string, error) {
	var s string
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return "", fmt.Errorf("decode token frame data %q: %w", data, err)
	}
	return s, nil
}

// decodeObjectData decodes a "done"/"error"/"sources" frame's data, which the
// server always marshals as a JSON object (writeSSE(w, event, map[string]any{...})).
func decodeObjectData(data string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, fmt.Errorf("decode object frame data %q: %w", data, err)
	}
	return m, nil
}
