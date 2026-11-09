package controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/authz"
	"stonesuite-backend/reports"
)

var interpretToday = time.Date(2026, time.November, 6, 9, 0, 0, 0, time.UTC)

type modelCallRecord struct {
	system, user string
	schema       string
}

// scriptedModel answers each call with the next canned reply (or error) and
// records what it was asked.
type scriptedModel struct {
	replies []string
	errs    []error
	calls   []modelCallRecord
}

func (m *scriptedModel) call(_ context.Context, system, user string, schema []byte, _ int) (string, error) {
	i := len(m.calls)
	m.calls = append(m.calls, modelCallRecord{system, user, string(schema)})
	if i < len(m.errs) && m.errs[i] != nil {
		return "", m.errs[i]
	}
	if i < len(m.replies) {
		return m.replies[i], nil
	}
	return "", errors.New("unexpected extra model call")
}

func salesSources(t *testing.T) []reports.Source {
	t.Helper()
	access := buildCustomAccess([]authz.Grant{{Resource: authz.ResourceReportSales, Action: authz.ActionRead, Scope: authz.ScopeAll}}, true)
	return access.sources()
}

const groupedReply = `{"summary":"Revenue by customer","source":"invoice_lines","columns":[],"filters":[],
	"groupBy":{"field":"customer.name","dateBucket":""},"totals":[{"fn":"sum","field":"net_amount"}],
	"sort":{"key":"total:0","desc":true},"topN":10,"chart":"bar"}`

func TestRunInterpretationMakesTwoSmallCalls(t *testing.T) {
	m := &scriptedModel{replies: []string{`{"source":"invoice_lines"}`, groupedReply}}
	got, err := runInterpretation(context.Background(), m.call, salesSources(t), "top 10 customers by revenue", nil, interpretToday)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 2 {
		t.Fatalf("want 2 model calls (source, then definition), got %d", len(m.calls))
	}
	if strings.Contains(m.calls[0].schema, `"leads"`) || !strings.Contains(m.calls[0].schema, `"invoice_lines"`) {
		t.Errorf("call 1 must only offer the caller's sources: %s", m.calls[0].schema)
	}
	if strings.Contains(m.calls[0].system, "net_amount") {
		t.Error("call 1 must not carry a source's fields")
	}
	if !strings.Contains(m.calls[1].system, "net_amount") || !strings.Contains(m.calls[1].system, "2026-11-06") {
		t.Errorf("call 2 must carry the chosen source's fields and today's date:\n%s", m.calls[1].system)
	}
	if m.calls[1].user != "top 10 customers by revenue" {
		t.Errorf("the user's own words go to the model unchanged, got %q", m.calls[1].user)
	}
	if got.Definition.Source != "invoice_lines" || got.Definition.TopN != 10 || got.Summary != "Revenue by customer" {
		t.Errorf("interpretation = %+v", got)
	}
}

func TestRunInterpretationEditSkipsSourceChoice(t *testing.T) {
	m := &scriptedModel{replies: []string{groupedReply}}
	current := &reports.Definition{Source: "invoice_lines", Columns: []string{"customer.name"}, Chart: reports.ChartNone}
	if _, err := runInterpretation(context.Background(), m.call, salesSources(t), "only paid", current, interpretToday); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("an edit keeps its source, so only the definition call is made; got %d calls", len(m.calls))
	}
	if !strings.Contains(m.calls[0].system, `"columns":["customer.name"]`) {
		t.Errorf("the current report must be in the prompt:\n%s", m.calls[0].system)
	}
}

func TestRunInterpretationRefusesASourceTheCallerCannotUse(t *testing.T) {
	m := &scriptedModel{}
	current := &reports.Definition{Source: "vendors", Columns: []string{"name"}, Chart: reports.ChartNone}
	_, err := runInterpretation(context.Background(), m.call, salesSources(t), "x", current, interpretToday)
	if !errors.Is(err, reports.ErrUninterpretable) || len(m.calls) != 0 {
		t.Fatalf("want ErrUninterpretable and no model call, got %v after %d calls", err, len(m.calls))
	}
}

func TestRunInterpretationPassesModelErrorsThrough(t *testing.T) {
	m := &scriptedModel{errs: []error{ragcore.ErrUnavailable}}
	_, err := runInterpretation(context.Background(), m.call, salesSources(t), "x", nil, interpretToday)
	if !errors.Is(err, ragcore.ErrUnavailable) {
		t.Fatalf("model errors must reach classifyAIError untouched, got %v", err)
	}
}

func TestRunInterpretationUninterpretable(t *testing.T) {
	for name, replies := range map[string][]string{
		"garbage source":     {"I cannot help"},
		"unknown source":     {`{"source":"leads"}`},
		"garbage definition": {`{"source":"invoice_lines"}`, "not json"},
	} {
		t.Run(name, func(t *testing.T) {
			m := &scriptedModel{replies: replies}
			_, err := runInterpretation(context.Background(), m.call, salesSources(t), "x", nil, interpretToday)
			if !errors.Is(err, reports.ErrUninterpretable) {
				t.Fatalf("want ErrUninterpretable, got %v", err)
			}
		})
	}
}

func TestValidateInterpretPrompt(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"trimmed", "  revenue by customer \n", "revenue by customer", false},
		{"empty", "", "", true},
		{"blank", "  \n ", "", true},
		{"exactly the limit", strings.Repeat("a", maxInterpretPromptBytes), strings.Repeat("a", maxInterpretPromptBytes), false},
		{"over the limit", strings.Repeat("a", maxInterpretPromptBytes+1), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateInterpretPrompt(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v; want %q err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
	if maxInterpretPromptBytes != 2000 {
		t.Errorf("the prompt limit is part of the contract (2000 bytes), got %d", maxInterpretPromptBytes)
	}
}

func TestInterpretErrorStatus(t *testing.T) {
	status, code, msg := interpretErrorStatus(reports.ErrUninterpretable)
	if status != http.StatusUnprocessableEntity || code != codeUninterpretable || !strings.Contains(msg, "couldn't turn that into a report") {
		t.Errorf("uninterpretable = %d %q %q", status, code, msg)
	}
	status, code, msg = interpretErrorStatus(ragcore.ErrUnavailable)
	if status != http.StatusServiceUnavailable || code != codeUnavailable || !strings.Contains(msg, "build the report by hand") {
		t.Errorf("unavailable = %d %q %q", status, code, msg)
	}
	status, code, _ = interpretErrorStatus(context.DeadlineExceeded)
	if status != http.StatusGatewayTimeout || code != codeTimeout {
		t.Errorf("timeout = %d %q", status, code)
	}
	var verr *reports.ValidationError
	status, _, _ = interpretErrorStatus(errors.Join(errors.New("x"), verr))
	if status == http.StatusUnprocessableEntity {
		t.Error("only ErrUninterpretable maps to 422")
	}
}
