package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaLifecycleIsConfigured(t *testing.T) {
	if (&OllamaLifecycle{}).IsConfigured() {
		t.Fatal("empty token must report unconfigured")
	}
	if !NewOllamaLifecycle("app", "tok").IsConfigured() {
		t.Fatal("non-empty token must report configured")
	}
}

func TestOllamaLifecycleStartAllStartsEveryMachine(t *testing.T) {
	var started []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization header = %q", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/machines") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]flyMachine{{ID: "m1"}, {ID: "m2"}})
		case strings.HasSuffix(r.URL.Path, "/start") && r.Method == http.MethodPost:
			started = append(started, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/apps/app1/machines/"), "/start"))
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	o := NewOllamaLifecycle("app1", "test-token")
	o.client = srv.Client()
	overrideBase(t, srv.URL)

	if err := o.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(started) != 2 || started[0] != "m1" || started[1] != "m2" {
		t.Fatalf("started = %v, want [m1 m2]", started)
	}
}

func TestOllamaLifecycleStopAllContinuesAfterOneFailure(t *testing.T) {
	var stopped []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/machines") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]flyMachine{{ID: "bad"}, {ID: "good"}})
		case strings.HasSuffix(r.URL.Path, "/stop") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/apps/app1/machines/"), "/stop")
			if id == "bad" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			stopped = append(stopped, id)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	o := NewOllamaLifecycle("app1", "test-token")
	o.client = srv.Client()
	overrideBase(t, srv.URL)

	err := o.StopAll(context.Background())
	if err == nil {
		t.Fatal("expected the 'bad' machine's failure to surface")
	}
	if len(stopped) != 1 || stopped[0] != "good" {
		t.Fatalf("stopped = %v, want [good] (one failure must not block the rest)", stopped)
	}
}

func TestOllamaLifecycleListMachinesPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid token"))
	}))
	defer srv.Close()

	o := NewOllamaLifecycle("app1", "bad-token")
	o.client = srv.Client()
	overrideBase(t, srv.URL)

	if err := o.StartAll(context.Background()); err == nil {
		t.Fatal("expected error on 401 from list machines")
	}
}

// overrideBase points flyMachinesAPIBase at the test server for the duration
// of the test, restoring the real endpoint afterward.
func overrideBase(t *testing.T, base string) {
	t.Helper()
	orig := flyMachinesAPIBase
	flyMachinesAPIBase = base + "/v1"
	t.Cleanup(func() { flyMachinesAPIBase = orig })
}
