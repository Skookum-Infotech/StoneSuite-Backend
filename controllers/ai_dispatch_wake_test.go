package controllers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

type fakeWaker struct {
	calls int
	err   error
}

func (f *fakeWaker) EnsureRunning(context.Context) error {
	f.calls++
	return f.err
}

type captureSink struct {
	retrieved int
	tokens    []string
}

func (c *captureSink) OnRetrieved([]ragcore.Citation) error { c.retrieved++; return nil }
func (c *captureSink) OnToken(t string) error               { c.tokens = append(c.tokens, t); return nil }

var errDown = fmt.Errorf("embed: %w", ragcore.ErrUnavailable)

// flaky fails with errDown for the first `failures` attempts, then succeeds.
// Each attempt reports its sources first, like a real AskStream.
func flaky(failures int, tokensBeforeFail int) (func(ragcore.StreamSink) (ragcore.AskResult, error), *int) {
	n := 0
	return func(s ragcore.StreamSink) (ragcore.AskResult, error) {
		n++
		if s != nil {
			_ = s.OnRetrieved(nil)
		}
		if n <= failures {
			if s != nil {
				for i := 0; i < tokensBeforeFail; i++ {
					_ = s.OnToken("x")
				}
			}
			return ragcore.AskResult{}, errDown
		}
		if s != nil {
			_ = s.OnToken("hello")
		}
		return ragcore.AskResult{Answer: "hello"}, nil
	}, &n
}

func TestWithOllamaWake(t *testing.T) {
	ctx := context.Background()

	t.Run("wakes and retries once, delivering sources only once", func(t *testing.T) {
		w := &fakeWaker{}
		h := &AIOps{waker: w}
		attempt, n := flaky(1, 0)
		sink := &captureSink{}

		res, err := h.withOllamaWake(ctx, sink, attempt)

		require.NoError(t, err)
		assert.Equal(t, "hello", res.Answer)
		assert.Equal(t, 1, w.calls)
		assert.Equal(t, 2, *n)
		assert.Equal(t, 1, sink.retrieved, "the retry must not resend sources")
		assert.Equal(t, []string{"hello"}, sink.tokens)
	})

	t.Run("non-streaming passes a nil sink and retries", func(t *testing.T) {
		w := &fakeWaker{}
		h := &AIOps{waker: w}
		attempt, n := flaky(1, 0)

		res, err := h.withOllamaWake(ctx, nil, attempt)

		require.NoError(t, err)
		assert.Equal(t, "hello", res.Answer)
		assert.Equal(t, 2, *n)
	})

	t.Run("wake failure returns the original unavailable error", func(t *testing.T) {
		w := &fakeWaker{err: errors.New("cooldown")}
		h := &AIOps{waker: w}
		attempt, n := flaky(5, 0)

		_, err := h.withOllamaWake(ctx, nil, attempt)

		assert.ErrorIs(t, err, ragcore.ErrUnavailable)
		assert.Equal(t, 1, *n, "no retry when the box could not be started")
	})

	t.Run("still unavailable after the wake is returned, not retried again", func(t *testing.T) {
		w := &fakeWaker{}
		h := &AIOps{waker: w}
		attempt, n := flaky(5, 0)

		_, err := h.withOllamaWake(ctx, nil, attempt)

		assert.ErrorIs(t, err, ragcore.ErrUnavailable)
		assert.Equal(t, 2, *n)
		assert.Equal(t, 1, w.calls)
	})

	t.Run("no retry once the client has received tokens", func(t *testing.T) {
		w := &fakeWaker{}
		h := &AIOps{waker: w}
		attempt, n := flaky(1, 1)

		_, err := h.withOllamaWake(ctx, &captureSink{}, attempt)

		assert.ErrorIs(t, err, ragcore.ErrUnavailable)
		assert.Equal(t, 1, *n)
		assert.Zero(t, w.calls)
	})

	t.Run("other errors are not retried", func(t *testing.T) {
		w := &fakeWaker{}
		h := &AIOps{waker: w}
		boom := errors.New("boom")
		calls := 0

		_, err := h.withOllamaWake(ctx, nil, func(ragcore.StreamSink) (ragcore.AskResult, error) {
			calls++
			return ragcore.AskResult{}, boom
		})

		assert.ErrorIs(t, err, boom)
		assert.Equal(t, 1, calls)
		assert.Zero(t, w.calls)
	})

	t.Run("no waker configured means no retry", func(t *testing.T) {
		h := &AIOps{}
		attempt, n := flaky(1, 0)

		_, err := h.withOllamaWake(ctx, nil, attempt)

		assert.ErrorIs(t, err, ragcore.ErrUnavailable)
		assert.Equal(t, 1, *n)
	})
}
