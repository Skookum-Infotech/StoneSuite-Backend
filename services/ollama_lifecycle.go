package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// flyMachinesAPIBase is Fly's Machines REST API. See
// https://fly.io/docs/machines/api/. A var, not a const, so tests can point
// it at an httptest server.
var flyMachinesAPIBase = "https://api.machines.dev/v1"

// OllamaLifecycle starts/stops every Machine in one Fly app via the Machines
// API, scoped to a token with rights to just that app. Used to tie the
// self-hosted embedder box's uptime to the backend's own process lifetime:
// Fly Proxy's flycast autostart was verified unreliable for this deployment
// (see docs/ai-assistant.md), so the backend controls it explicitly instead.
type OllamaLifecycle struct {
	appName string
	token   string
	client  *http.Client
}

// NewOllamaLifecycle builds a lifecycle controller for the given Fly app.
func NewOllamaLifecycle(appName, token string) *OllamaLifecycle {
	return &OllamaLifecycle{
		appName: appName,
		token:   token,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// IsConfigured reports whether a token was supplied — callers should skip
// lifecycle control entirely when it wasn't (e.g. local dev, or an always-on
// embedder box with no backend-driven start/stop).
func (o *OllamaLifecycle) IsConfigured() bool { return o.token != "" }

type flyMachine struct {
	ID string `json:"id"`
}

// StartAll starts every Machine in the app. Best-effort per machine: one
// Machine's failure doesn't stop the others from starting.
func (o *OllamaLifecycle) StartAll(ctx context.Context) error {
	return o.forEachMachine(ctx, "start")
}

// StopAll stops every Machine in the app. Best-effort per machine.
func (o *OllamaLifecycle) StopAll(ctx context.Context) error {
	return o.forEachMachine(ctx, "stop")
}

func (o *OllamaLifecycle) forEachMachine(ctx context.Context, action string) error {
	machines, err := o.listMachines(ctx)
	if err != nil {
		return fmt.Errorf("list machines: %w", err)
	}
	var firstErr error
	for _, m := range machines {
		url := fmt.Sprintf("%s/apps/%s/machines/%s/%s", flyMachinesAPIBase, o.appName, m.ID, action)
		if err := o.post(ctx, url); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s machine %s: %w", action, m.ID, err)
		}
	}
	return firstErr
}

func (o *OllamaLifecycle) listMachines(ctx context.Context) ([]flyMachine, error) {
	url := fmt.Sprintf("%s/apps/%s/machines", flyMachinesAPIBase, o.appName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.token)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var machines []flyMachine
	if err := json.Unmarshal(body, &machines); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return machines, nil
}

func (o *OllamaLifecycle) post(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.token)

	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
