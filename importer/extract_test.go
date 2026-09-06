package importer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/workflow"
)

// plainLLM implements only rag.LLMClient — no ChatJSON — to exercise the
// ErrExtractionUnsupported path.
type plainLLM struct{}

func (plainLLM) Chat(ctx context.Context, system string, messages []ragcore.Message) (string, error) {
	return "", errors.New("plainLLM.Chat should never be called by ExtractFields")
}

// fakeStructuredLLM implements rag.StructuredLLMClient with a canned reply.
type fakeStructuredLLM struct {
	reply       string
	err         error
	lastSchema  []byte
	lastMessage string
}

func (f *fakeStructuredLLM) Chat(ctx context.Context, system string, messages []ragcore.Message) (string, error) {
	return "", errors.New("fakeStructuredLLM.Chat should not be called; ExtractFields must use ChatJSON")
}

func (f *fakeStructuredLLM) ChatJSON(ctx context.Context, system string, messages []ragcore.Message, schema []byte) (string, error) {
	f.lastSchema = schema
	if len(messages) > 0 {
		f.lastMessage = messages[0].Content
	}
	return f.reply, f.err
}

func TestExtractFields_UnsupportedLLM(t *testing.T) {
	defs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber}}
	_, err := ExtractFields(context.Background(), plainLLM{}, defs, "some text")
	assert.ErrorIs(t, err, ErrExtractionUnsupported)
}

func TestExtractFields_NoFieldsShortCircuits(t *testing.T) {
	llm := &fakeStructuredLLM{reply: `{"should":"not be read"}`}
	out, err := ExtractFields(context.Background(), llm, nil, "some text")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Empty(t, llm.lastMessage, "ExtractFields must not call the LLM when there are no fields to extract")
}

func TestExtractFields_ParsesStructuredReply(t *testing.T) {
	defs := []workflow.FieldDefinition{
		{Key: "budget", DataType: workflow.TypeNumber},
		{Key: "priority", DataType: workflow.TypeEnum, Options: []string{"low", "high"}},
	}
	llm := &fakeStructuredLLM{reply: `{"budget": 5000, "priority": "high"}`}

	out, err := ExtractFields(context.Background(), llm, defs, "Budget is 5000, priority high.")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"budget": 5000.0, "priority": "high"}, out)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(llm.lastSchema, &schema))
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "budget")
	assert.Contains(t, props, "priority")
	priorityProp, ok := props["priority"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"low", "high"}, priorityProp["enum"])
}

func TestExtractFields_MalformedReplyErrors(t *testing.T) {
	defs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber}}
	llm := &fakeStructuredLLM{reply: "not json"}
	_, err := ExtractFields(context.Background(), llm, defs, "text")
	assert.Error(t, err)
}

func TestExtractFields_LLMErrorPropagates(t *testing.T) {
	defs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber}}
	llm := &fakeStructuredLLM{err: errors.New("model unavailable")}
	_, err := ExtractFields(context.Background(), llm, defs, "text")
	assert.Error(t, err)
}

func TestJSONSchemaType(t *testing.T) {
	tests := []struct {
		dt   workflow.DataType
		want string
	}{
		{workflow.TypeNumber, "number"},
		{workflow.TypeBool, "boolean"},
		{workflow.TypeString, "string"},
		{workflow.TypeDate, "string"},
		{workflow.TypeEmail, "string"},
		{workflow.TypeEnum, "string"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, jsonSchemaType(tc.dt))
	}
}
