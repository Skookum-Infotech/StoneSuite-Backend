package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLLM reports usage the way the Ollama provider does and records the
// max-token bound it was given.
type fakeLLM struct {
	usage     ragcore.Usage
	err       error
	maxTokens int
}

func (f *fakeLLM) Chat(ctx context.Context, _ string, _ []ragcore.Message) (string, error) {
	ragcore.ReportUsage(ctx, f.usage)
	return "chat", f.err
}

func (f *fakeLLM) ChatJSON(ctx context.Context, _ string, _ []ragcore.Message, _ []byte) (string, error) {
	ragcore.ReportUsage(ctx, f.usage)
	return "{}", f.err
}

func (f *fakeLLM) ChatStream(ctx context.Context, _ string, _ []ragcore.Message, onToken func(string) error) (string, error) {
	_ = onToken("tok")
	ragcore.ReportUsage(ctx, f.usage)
	return "stream", f.err
}

func (f *fakeLLM) WithMaxTokens(n int) ragcore.LLMClient {
	cp := *f
	cp.maxTokens = n
	return &cp
}

// chatOnly implements only the base LLMClient.
type chatOnly struct{}

func (chatOnly) Chat(context.Context, string, []ragcore.Message) (string, error) { return "", nil }

type fakeRecorder struct {
	mu      sync.Mutex
	entries []TokenEntry
}

func (r *fakeRecorder) Record(e TokenEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

var fixedNow = time.Date(2026, 11, 16, 15, 4, 5, 0, time.UTC)

func newMetered(inner ragcore.LLMClient, rec entryRecorder) *MeteredLLM {
	m := NewMeteredLLM(inner, "qwen-test", rec)
	m.now = func() time.Time { return fixedNow }
	return m
}

func TestMeteredLLM_RecordsEachCallKind(t *testing.T) {
	calls := map[string]func(m *MeteredLLM, ctx context.Context) (string, error){
		"chat": func(m *MeteredLLM, ctx context.Context) (string, error) { return m.Chat(ctx, "s", nil) },
		"json": func(m *MeteredLLM, ctx context.Context) (string, error) { return m.ChatJSON(ctx, "s", nil, nil) },
		"stream": func(m *MeteredLLM, ctx context.Context) (string, error) {
			return m.ChatStream(ctx, "s", nil, func(string) error { return nil })
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			rec := &fakeRecorder{}
			m := newMetered(&fakeLLM{usage: ragcore.Usage{PromptTokens: 100, CompletionTokens: 40}}, rec)
			ctx := WithSource(WithTenant(context.Background(), "tenant-a"), SourceAsk)
			_, err := call(m, ctx)
			require.NoError(t, err)
			require.Len(t, rec.entries, 1)
			assert.Equal(t, TokenEntry{
				TenantID: "tenant-a", Day: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
				Source: SourceAsk, Model: "qwen-test", PromptTokens: 100, CompletionTokens: 40,
			}, rec.entries[0])
		})
	}
}

func TestMeteredLLM_ReReportsUsageToCallerSlot(t *testing.T) {
	m := newMetered(&fakeLLM{usage: ragcore.Usage{PromptTokens: 7, CompletionTokens: 3, Truncated: true}}, &fakeRecorder{})
	ctx, slot := ragcore.WithUsage(WithTenant(context.Background(), "tenant-a"))
	_, err := m.Chat(ctx, "s", nil)
	require.NoError(t, err)
	assert.Equal(t, ragcore.Usage{PromptTokens: 7, CompletionTokens: 3, Truncated: true}, *slot)
}

func TestMeteredLLM_SkipCases(t *testing.T) {
	tests := []struct {
		name  string
		ctx   context.Context
		usage ragcore.Usage
	}{
		{"no tenant (platform work)", context.Background(), ragcore.Usage{PromptTokens: 5}},
		{"warm-up is never billed", WithSource(WithTenant(context.Background(), "t"), SourceWarmup), ragcore.Usage{PromptTokens: 5}},
		{"zero tokens reported", WithTenant(context.Background(), "t"), ragcore.Usage{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeRecorder{}
			_, err := newMetered(&fakeLLM{usage: tc.usage}, rec).Chat(tc.ctx, "s", nil)
			require.NoError(t, err)
			assert.Empty(t, rec.entries)
		})
	}
}

func TestMeteredLLM_RecordsTokensEvenWhenCallErrors(t *testing.T) {
	rec := &fakeRecorder{}
	boom := errors.New("stream cut")
	_, err := newMetered(&fakeLLM{usage: ragcore.Usage{PromptTokens: 9}, err: boom}, rec).
		ChatStream(WithTenant(context.Background(), "t"), "s", nil, func(string) error { return nil })
	assert.ErrorIs(t, err, boom)
	require.Len(t, rec.entries, 1)
	assert.Equal(t, SourceOther, rec.entries[0].Source, "an untagged call is recorded as other")
}

func TestMeteredLLM_WithMaxTokensStaysMetered(t *testing.T) {
	rec := &fakeRecorder{}
	bounded := newMetered(&fakeLLM{usage: ragcore.Usage{CompletionTokens: 2}}, rec).WithMaxTokens(64)
	metered, ok := bounded.(*MeteredLLM)
	require.True(t, ok, "WithMaxTokens must return a metered client")
	assert.Equal(t, 64, metered.inner.(*fakeLLM).maxTokens)
	_, err := bounded.Chat(WithTenant(context.Background(), "t"), "s", nil)
	require.NoError(t, err)
	assert.Len(t, rec.entries, 1)
}

func TestMeteredLLM_UnsupportedCapabilities(t *testing.T) {
	m := newMetered(chatOnly{}, &fakeRecorder{})
	_, err := m.ChatJSON(context.Background(), "s", nil, nil)
	assert.ErrorIs(t, err, errNotSupported)
	_, err = m.ChatStream(context.Background(), "s", nil, nil)
	assert.ErrorIs(t, err, errNotSupported)
	assert.Same(t, m, m.WithMaxTokens(1))
}
