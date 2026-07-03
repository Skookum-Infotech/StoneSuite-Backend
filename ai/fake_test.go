package ai

import (
	"context"
	"testing"
)

// Compile-time proof the fakes satisfy the consumer interfaces.
var _ Embedder = (*FakeEmbedder)(nil)
var _ LLMClient = (*FakeLLM)(nil)

func TestFakeEmbedderReturnsCannedVectors(t *testing.T) {
	fe := &FakeEmbedder{Dim: 3}
	vecs, err := fe.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 {
		t.Fatalf("got %d vectors of dim %d, want 2 of dim 3", len(vecs), len(vecs[0]))
	}
}

func TestFakeLLMEchoesReply(t *testing.T) {
	fl := &FakeLLM{Reply: "hello"}
	out, err := fl.Chat(context.Background(), "sys", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("Chat = %q, want hello", out)
	}
	if len(fl.GotMessages) != 1 {
		t.Fatalf("recorded %d messages, want 1", len(fl.GotMessages))
	}
}
