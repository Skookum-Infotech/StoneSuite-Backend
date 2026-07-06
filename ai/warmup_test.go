package ai

import (
	"context"
	"testing"
)

func TestWarmUpCallsEmbedThenChat(t *testing.T) {
	emb := &FakeEmbedder{Dim: 768}
	llm := &FakeLLM{Reply: "ok"}
	if err := WarmUp(context.Background(), emb, llm); err != nil {
		t.Fatal(err)
	}
	if len(llm.GotMessages) != 1 {
		t.Fatalf("want 1 chat call, got %d", len(llm.GotMessages))
	}
}

func TestWarmUpPropagatesEmbedError(t *testing.T) {
	emb := &FakeEmbedder{Err: errBoom}
	llm := &FakeLLM{Reply: "ok"}
	if err := WarmUp(context.Background(), emb, llm); err == nil {
		t.Fatal("expected embed error to propagate")
	}
}

func TestWarmUpPropagatesChatError(t *testing.T) {
	emb := &FakeEmbedder{Dim: 768}
	llm := &FakeLLM{Err: errBoom}
	if err := WarmUp(context.Background(), emb, llm); err == nil {
		t.Fatal("expected chat error to propagate")
	}
}
