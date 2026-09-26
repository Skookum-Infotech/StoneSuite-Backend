package ai

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These lock in Conversation/Message's JSON shape as camelCase, matching the
// rest of the API (e.g. importJobSummary) — both types previously had no
// json tags at all and serialized with capitalized Go field names.
func TestConversationMarshalsCamelCase(t *testing.T) {
	conv := Conversation{
		ID: "conv-1", OwnerUserID: "user-1", Title: "Test",
		CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}
	body, err := json.Marshal(conv)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	assert.ElementsMatch(t, []string{"id", "ownerUserId", "title", "createdAt", "updatedAt"}, keysOf(out))
}

func TestMessageMarshalsCamelCase(t *testing.T) {
	msg := Message{Role: "user", Content: "hi", CreatedAt: time.Unix(0, 0).UTC()}
	body, err := json.Marshal(msg)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	assert.ElementsMatch(t, []string{"role", "content", "createdAt"}, keysOf(out))
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTrimHistory(t *testing.T) {
	u := func(n int) rag.Message { return rag.Message{Role: "user", Content: strings.Repeat("u", n)} }
	a := func(n int) rag.Message { return rag.Message{Role: "assistant", Content: strings.Repeat("a", n)} }
	roles := func(ms []rag.Message) string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Role[:1]+strconv.Itoa(len(m.Content)))
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name   string
		in     []rag.Message
		budget int
		want   string
	}{
		{"fits", []rag.Message{u(1), a(2), u(3), a(4)}, 100, "u1,a2,u3,a4"},
		{"cut whole oldest pair", []rag.Message{u(5), a(5), u(5), a(5)}, 12, "u5,a5"},
		{"never starts with assistant after cut", []rag.Message{u(9), a(1), u(2), a(2)}, 6, "u2,a2"},
		{"leading orphan assistant dropped even when it fits", []rag.Message{a(2), u(2), a(2)}, 100, "u2,a2"},
		{"oversized newest answer drops its pair", []rag.Message{u(1), a(1), u(1), a(50)}, 10, ""},
		{"empty", nil, 10, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimHistory(tt.in, tt.budget)
			assert.Equal(t, tt.want, roles(got))
			if len(got) > 0 {
				assert.Equal(t, "user", got[0].Role)
			}
		})
	}
}
