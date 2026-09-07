package rag

import "context"

// FakeEmbedder returns deterministic canned vectors of width Dim. Test-only,
// but exported so later plans' tests can reuse it.
type FakeEmbedder struct {
	Dim int
	Err error
}

// Embed returns one Dim-wide vector per input; vector[i][0] encodes the index
// so tests can assert ordering. Returns Err if set.
func (f *FakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.Dim)
		if f.Dim > 0 {
			v[0] = float32(i)
		}
		out[i] = v
	}
	return out, nil
}

// FakeLLM returns a fixed Reply and records what it was asked.
type FakeLLM struct {
	Reply       string
	Err         error
	GotSystem   string
	GotMessages []Message
}

// Chat records the inputs and returns the canned Reply (or Err if set).
func (f *FakeLLM) Chat(_ context.Context, system string, messages []Message) (string, error) {
	f.GotSystem = system
	f.GotMessages = messages
	if f.Err != nil {
		return "", f.Err
	}
	return f.Reply, nil
}

// FakeReranker reverses candidate order (a cheap, deterministic way for a
// test to prove reranked order — not RRF order — reached the final answer)
// and truncates to n, or returns Err if set. Records what it was asked.
type FakeReranker struct {
	Err           error
	GotQuestion   string
	GotCandidates []Citation
	CalledWithN   int
}

func (f *FakeReranker) Rerank(_ context.Context, question string, candidates []Citation, n int) ([]Citation, error) {
	f.GotQuestion = question
	f.GotCandidates = candidates
	f.CalledWithN = n
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]Citation, len(candidates))
	for i, c := range candidates {
		out[len(candidates)-1-i] = c
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out, nil
}
