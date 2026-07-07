package ai

import (
	"strings"
	"testing"
)

func TestSnippet(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short text unchanged", "hello world", "hello world"},
		{"newlines collapsed to spaces", "line one\nline two", "line one line two"},
		{"exactly 240 chars unchanged", strings.Repeat("a", 240), strings.Repeat("a", 240)},
		{"over 240 chars truncated with ellipsis", strings.Repeat("a", 241), strings.Repeat("a", 240) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snippet(tt.input); got != tt.want {
				t.Fatalf("snippet(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGroundingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short text unchanged", "hello world", "hello world"},
		{"newlines preserved", "line one\nline two", "line one\nline two"},
		{"surrounding whitespace trimmed", "  hello  ", "hello"},
		{"exactly groundingLimit chars unchanged", strings.Repeat("a", groundingLimit), strings.Repeat("a", groundingLimit)},
		{"over groundingLimit truncated with ellipsis", strings.Repeat("a", groundingLimit+1), strings.Repeat("a", groundingLimit) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := groundingContent(tt.input); got != tt.want {
				t.Fatalf("groundingContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
