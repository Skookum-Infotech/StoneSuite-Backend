// Package logship forwards structured (slog JSON) logs to Axiom over HTTP,
// without a dedicated log-shipper VM — keeping the deployment compatible with
// scale-to-zero and at zero extra infrastructure cost.
//
// Design: a Shipper is an io.Writer placed alongside os.Stdout via
// io.MultiWriter, so the slog JSON handler's already-formatted, newline-
// delimited records are both printed (for `fly logs`) and copied into a bounded
// channel. A single background worker batches those lines and POSTs them to
// Axiom's ingest API as application/x-ndjson, flushing on a timer or when a
// batch fills.
//
// Reliability rules:
//   - Write never blocks and never errors — if the buffer is full it drops the
//     line, so logging can never slow or break the request path.
//   - The worker NEVER logs via slog (that would recurse into shipping); it
//     reports its own failures to os.Stderr only.
//   - The worker drains and does a final flush on context cancellation, and
//     Stop() waits for it, so shutdown loses at most the in-flight HTTP batch.
package logship

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	// bufferSize bounds in-memory log lines awaiting shipment. Beyond this,
	// lines are dropped rather than blocking request handling.
	bufferSize = 4096
	// batchMax is the most lines sent in a single ingest request.
	batchMax = 256
	// flushInterval is the longest a buffered line waits before being shipped.
	flushInterval = 3 * time.Second
	// httpTimeout bounds each ingest POST.
	httpTimeout = 5 * time.Second
)

// Shipper batches slog JSON lines and ships them to Axiom. The zero value is
// not usable; construct with New.
type Shipper struct {
	url    string
	token  string
	client *http.Client
	ch     chan []byte
	wg     sync.WaitGroup
	// dropped counts lines discarded due to a full buffer (best-effort, for an
	// occasional stderr notice — not exact under contention).
	dropped int
	mu      sync.Mutex
}

// New returns a Shipper for the given Axiom dataset, or nil when either the
// token or dataset is empty (feature disabled — caller logs only to stdout).
func New(token, dataset string) *Shipper {
	if token == "" || dataset == "" {
		return nil
	}
	return &Shipper{
		url:    fmt.Sprintf("https://api.axiom.co/v1/datasets/%s/ingest", dataset),
		token:  token,
		client: &http.Client{Timeout: httpTimeout},
		ch:     make(chan []byte, bufferSize),
	}
}

// Start launches the background shipping worker. The worker exits (after a final
// drain + flush) when ctx is cancelled. Pass the server's shutdown context.
func (s *Shipper) Start(ctx context.Context) {
	s.wg.Add(1)
	go s.worker(ctx)
}

// Stop waits for the worker to finish its final flush. Call during shutdown,
// after the shutdown context has been cancelled.
func (s *Shipper) Stop() { s.wg.Wait() }

// Write implements io.Writer. It copies p (slog reuses its buffer) and enqueues
// it without blocking; a full buffer drops the line. It always reports success
// so io.MultiWriter never aborts the write to stdout.
func (s *Shipper) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case s.ch <- b:
	default:
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
	}
	return len(p), nil
}

func (s *Shipper) worker(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([][]byte, 0, batchMax)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.send(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Drain whatever is buffered, then flush and exit.
			for {
				select {
				case b := <-s.ch:
					batch = append(batch, b)
					if len(batch) >= batchMax {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case b := <-s.ch:
			batch = append(batch, b)
			if len(batch) >= batchMax {
				flush()
			}
		case <-ticker.C:
			flush()
			if d := s.takeDropped(); d > 0 {
				fmt.Fprintf(os.Stderr, "logship: dropped %d log lines (buffer full)\n", d)
			}
		}
	}
}

// send POSTs a batch as newline-delimited JSON. Each line already ends in '\n'
// (slog JSON handler), so concatenation yields valid ndjson.
func (s *Shipper) send(batch [][]byte) {
	var buf bytes.Buffer
	for _, b := range batch {
		buf.Write(b)
	}
	req, err := http.NewRequest(http.MethodPost, s.url, &buf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logship: build request: %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := s.client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logship: ship %d lines: %v\n", len(batch), err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "logship: ingest returned HTTP %d\n", resp.StatusCode)
	}
}

func (s *Shipper) takeDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dropped
	s.dropped = 0
	return d
}
