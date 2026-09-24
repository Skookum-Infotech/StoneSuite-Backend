package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStarter struct {
	calls atomic.Int32
	err   error
	delay time.Duration
}

func (f *fakeStarter) StartAll(_ context.Context) error {
	f.calls.Add(1)
	time.Sleep(f.delay)
	return f.err
}

// newTestWaker points a waker at an httptest server that answers 503 until
// readyAfter probes have been made, with test-speed timings.
func newTestWaker(t *testing.T, starter machineStarter, readyAfter int32) (*OllamaWaker, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		if probes.Add(1) <= readyAfter {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	w := NewOllamaWaker(starter, srv.URL)
	w.pollEvery = 5 * time.Millisecond
	w.maxWait = 300 * time.Millisecond
	w.cooldown = 200 * time.Millisecond
	return w, &probes
}

func TestOllamaWaker_ReturnsOnceServerAnswers(t *testing.T) {
	st := &fakeStarter{}
	w, probes := newTestWaker(t, st, 3)

	require.NoError(t, w.EnsureRunning(context.Background()))
	assert.EqualValues(t, 1, st.calls.Load())
	assert.GreaterOrEqual(t, probes.Load(), int32(4), "kept polling until ready")
}

func TestOllamaWaker_ConcurrentCallersShareOneAttempt(t *testing.T) {
	st := &fakeStarter{delay: 30 * time.Millisecond}
	w, _ := newTestWaker(t, st, 2)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = w.EnsureRunning(context.Background())
		}()
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, st.calls.Load(), "StartAll must run once for a burst of callers")
}

func TestOllamaWaker_StartErrorStillSucceedsWhenServerIsUp(t *testing.T) {
	st := &fakeStarter{err: errors.New("machine already started")}
	w, _ := newTestWaker(t, st, 0)

	require.NoError(t, w.EnsureRunning(context.Background()))
}

func TestOllamaWaker_TimeoutThenCooldownThenRetry(t *testing.T) {
	st := &fakeStarter{err: errors.New("no such app")}
	w, probes := newTestWaker(t, st, 1<<30) // never ready

	err := w.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such app")
	assert.EqualValues(t, 1, st.calls.Load())

	before := probes.Load()
	err = w.EnsureRunning(context.Background())
	assert.ErrorIs(t, err, ErrWakeCooldown)
	assert.EqualValues(t, 1, st.calls.Load(), "no Machines API call during cooldown")
	assert.Equal(t, before, probes.Load())

	time.Sleep(w.cooldown + 20*time.Millisecond)
	_ = w.EnsureRunning(context.Background())
	assert.EqualValues(t, 2, st.calls.Load(), "a new attempt is allowed after the cooldown")
}

func TestOllamaWaker_CallerCancelDoesNotStopSharedAttempt(t *testing.T) {
	st := &fakeStarter{}
	w, _ := newTestWaker(t, st, 20)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.EnsureRunning(ctx)
	assert.ErrorIs(t, err, context.Canceled)

	// The attempt continues in the background: a later caller joins it and
	// sees the server come up.
	require.NoError(t, w.EnsureRunning(context.Background()))
	assert.EqualValues(t, 1, st.calls.Load())
}

func TestOllamaWaker_SetEnabledFalseRefusesWithoutStarting(t *testing.T) {
	st := &fakeStarter{}
	w, _ := newTestWaker(t, st, 0)

	w.SetEnabled(false)
	err := w.EnsureRunning(context.Background())
	assert.ErrorIs(t, err, ErrWakeDisabled)
	assert.EqualValues(t, 0, st.calls.Load(), "StartAll must not be called while disabled")

	w.SetEnabled(true)
	require.NoError(t, w.EnsureRunning(context.Background()))
	assert.EqualValues(t, 1, st.calls.Load())
}
