package ai

import (
	"encoding/json"
	"testing"
	"time"

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
