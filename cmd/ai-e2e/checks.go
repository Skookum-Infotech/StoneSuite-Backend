package main

import (
	"fmt"
	"strings"
)

// refusalPhrase is the exact wording docs/assistant.md documents the
// assistant using when it can't find a good answer (see "If it says it
// doesn't have the information").
const refusalPhrase = "i don't have that information"

// evaluateCase checks res against c's expectations and returns whether it
// passed and, if not, a short description of the first failing check — in
// the same order the case schema lists them: status, contains, any,
// not-contains, refusal, route, min-sources. c must already have its
// placeholders resolved (see resolveCase).
func evaluateCase(c testCase, res askResult) (bool, string) {
	if c.ExpectStatus != 0 {
		if res.HTTPStatus != c.ExpectStatus {
			return false, fmt.Sprintf("expect_status: want %d, got %d", c.ExpectStatus, res.HTTPStatus)
		}
		// A case that expects a non-200 status is checking the rejection
		// itself; the body isn't a normal answer, so nothing else applies.
		return true, ""
	}

	if res.HTTPStatus != 0 && res.HTTPStatus != 200 {
		return false, fmt.Sprintf("unexpected http status %d", res.HTTPStatus)
	}
	if res.TransportErr != nil {
		return false, fmt.Sprintf("transport error: %v", res.TransportErr)
	}
	if res.ErrorPayload != nil {
		return false, fmt.Sprintf("unexpected error event: %v", res.ErrorPayload["message"])
	}

	answer := strings.ToLower(res.Answer)

	if missing, ok := allContain(answer, c.ExpectContains); !ok {
		return false, "expect_contains: missing " + missing
	}
	if len(c.ExpectAny) > 0 && !anyContain(answer, c.ExpectAny) {
		return false, fmt.Sprintf("expect_any: none of %v found", c.ExpectAny)
	}
	if found, ok := noneContain(answer, c.ExpectNotContains); !ok {
		return false, "expect_not_contains: found " + found
	}
	if c.ExpectRefusal && !strings.Contains(answer, refusalPhrase) {
		return false, "expect_refusal: refusal phrase not found"
	}
	if c.ExpectRoute != "" && res.Route != c.ExpectRoute {
		return false, fmt.Sprintf("expect_route: want %q, got %q", c.ExpectRoute, res.Route)
	}
	if c.ExpectMinSources > 0 && res.SourcesCount < c.ExpectMinSources {
		return false, fmt.Sprintf("expect_min_sources: want >=%d, got %d", c.ExpectMinSources, res.SourcesCount)
	}

	return true, ""
}

// allContain reports whether every string in want (case-insensitive) appears
// in answer (already lowercased), returning the first one that doesn't.
func allContain(answerLower string, want []string) (string, bool) {
	for _, w := range want {
		if !strings.Contains(answerLower, strings.ToLower(w)) {
			return w, false
		}
	}
	return "", true
}

// anyContain reports whether at least one string in want (case-insensitive)
// appears in answer (already lowercased).
func anyContain(answerLower string, want []string) bool {
	for _, w := range want {
		if strings.Contains(answerLower, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// noneContain reports whether no string in forbidden (case-insensitive)
// appears in answer (already lowercased), returning the first one that does.
func noneContain(answerLower string, forbidden []string) (string, bool) {
	for _, f := range forbidden {
		if strings.Contains(answerLower, strings.ToLower(f)) {
			return f, false
		}
	}
	return "", true
}
