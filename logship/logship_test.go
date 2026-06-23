package logship

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_DisabledWhenUnconfigured(t *testing.T) {
	assert.Nil(t, New("", "dataset"), "no token -> disabled")
	assert.Nil(t, New("token", ""), "no dataset -> disabled")
	s := New("token", "dataset")
	require.NotNil(t, s)
	assert.Equal(t, "https://api.axiom.co/v1/datasets/dataset/ingest", s.url)
}

func TestWrite_NeverErrorsAndCopies(t *testing.T) {
	s := New("token", "dataset")
	require.NotNil(t, s)
	line := []byte("{\"msg\":\"hi\"}\n")
	n, err := s.Write(line)
	assert.NoError(t, err)
	assert.Equal(t, len(line), n)
	// Mutating the caller's buffer must not affect the queued copy.
	line[0] = 'X'
	got := <-s.ch
	assert.Equal(t, "{\"msg\":\"hi\"}\n", string(got))
}

func TestWrite_DropsWhenFull(t *testing.T) {
	s := New("token", "dataset")
	require.NotNil(t, s)
	// Fill the buffer beyond capacity without a worker draining it.
	for i := 0; i < bufferSize+10; i++ {
		n, err := s.Write([]byte("x\n"))
		assert.NoError(t, err)
		assert.Equal(t, 2, n)
	}
	assert.GreaterOrEqual(t, s.takeDropped(), 10, "overflow lines should be counted as dropped, not block")
}

func TestShipper_ShipsNDJSONToIngest(t *testing.T) {
	var (
		mu          sync.Mutex
		gotBody     string
		gotAuth     string
		gotCT       string
		requestSeen = make(chan struct{}, 1)
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = string(body)
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		mu.Unlock()
		select {
		case requestSeen <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := New("secret-token", "ds")
	require.NotNil(t, s)
	s.url = srv.URL // redirect ingest to the test server

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	_, _ = s.Write([]byte("{\"a\":1}\n"))
	_, _ = s.Write([]byte("{\"a\":2}\n"))

	// Cancel to force an immediate drain + flush, then wait for the worker.
	cancel()
	s.Stop()

	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("ingest endpoint was never called")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "{\"a\":1}\n{\"a\":2}\n", gotBody, "lines should be concatenated as ndjson")
	assert.Equal(t, "Bearer secret-token", gotAuth)
	assert.Equal(t, "application/x-ndjson", gotCT)
}
