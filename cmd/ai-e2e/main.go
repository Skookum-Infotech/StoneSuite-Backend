package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// defaultBase is the dev backend this tool targets by default.
const defaultBase = "https://dev-stonesuite-api.fly.dev"

// defaultCasesPath is the bundled case file, relative to the repo root.
const defaultCasesPath = "cmd/ai-e2e/cases.json"

// defaultGap is the minimum spacing between requests. The AI per-user rate
// limiter (aiUserRateLimiter in main.go) allows ~1 request per 5s; 6s leaves
// headroom so a slow prior response never causes the next request to arrive
// early and get 429'd.
const defaultGap = 6 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("ai-e2e failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	base := flag.String("base", defaultBase, "backend base URL")
	tokenFile := flag.String("token-file", "", "path to a file containing a bare JWT (required)")
	casesPath := flag.String("cases", defaultCasesPath, "path to the cases JSON file")
	outPath := flag.String("out", "", "path to write full JSON results (required)")
	gap := flag.Duration("gap", defaultGap, "minimum delay between requests")
	only := flag.String("only", "", "comma-separated case ids to run (default: all)")
	flag.Parse()

	if *tokenFile == "" {
		return fmt.Errorf("-token-file is required")
	}
	if *outPath == "" {
		return fmt.Errorf("-out is required")
	}

	token, err := readToken(*tokenFile)
	if err != nil {
		return err
	}
	cases, err := loadCases(*casesPath)
	if err != nil {
		return err
	}
	if *only != "" {
		cases = filterCases(cases, strings.Split(*only, ","))
	}
	if len(cases) == 0 {
		return fmt.Errorf("no cases to run (check -cases and -only)")
	}

	ctx := context.Background()
	client := newAPIClient(*base, token)

	slog.Info("resolving placeholders", "base", *base)
	ph, err := client.fetchPlaceholders(ctx)
	if err != nil {
		return fmt.Errorf("resolve placeholders: %w", err)
	}

	results := runCases(ctx, client, cases, ph, *gap)

	printTable(results)
	printSummary(results)

	if err := writeResultsJSON(*outPath, results); err != nil {
		return err
	}
	slog.Info("wrote results", "path", *outPath)
	return nil
}

// readToken reads the bare JWT from path, trimmed of surrounding whitespace.
// The token is never logged or printed by this tool.
func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

// loadCases reads and decodes the cases JSON file.
func loadCases(path string) ([]testCase, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cases file %s: %w", path, err)
	}
	var cases []testCase
	if err := json.Unmarshal(b, &cases); err != nil {
		return nil, fmt.Errorf("decode cases file %s: %w", path, err)
	}
	return cases, nil
}

// filterCases keeps only the cases whose id is in ids. A follow-up case
// whose follow_up_of target was filtered out still runs with an empty
// conversation id (a fresh, single-turn ask) rather than being silently
// dropped.
func filterCases(cases []testCase, ids []string) []testCase {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var out []testCase
	for _, c := range cases {
		if want[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// runCases runs every case in order, honoring gap between requests and
// resolving follow_up_of to the conversation_id an earlier case in this run
// was assigned.
func runCases(ctx context.Context, client *apiClient, cases []testCase, ph map[string]string, gap time.Duration) []caseResult {
	results := make([]caseResult, 0, len(cases))
	conversationByCaseID := map[string]string{}

	for i, raw := range cases {
		if i > 0 {
			time.Sleep(gap)
		}
		c := resolveCase(raw, ph)

		conversationID := ""
		if c.FollowUpOf != "" {
			conversationID = conversationByCaseID[c.FollowUpOf]
		}

		slog.Info("running case", "id", c.ID, "category", c.Category)
		res := client.ask(ctx, c.Question, conversationID)
		if res.ConversationID != "" {
			conversationByCaseID[c.ID] = res.ConversationID
		}

		pass, firstFail := evaluateCase(c, res)
		results = append(results, caseResult{
			Case:        raw,
			Result:      res,
			Pass:        pass,
			FirstFail:   firstFail,
			TTFTMillis:  res.TTFT.Milliseconds(),
			TotalMillis: res.Total.Milliseconds(),
			Answer:      res.Answer,
			HTTPStatus:  res.HTTPStatus,
			Done:        firstNonNil(res.Done, res.StatusBodyRaw),
			Error:       res.ErrorPayload,
		})
	}
	return results
}

// firstNonNil returns the first of a, b that is non-nil (as a map), for
// caseResult.Done — either the SSE "done" payload or, for a request that
// failed before streaming started, the ordinary JSON error body.
func firstNonNil(a, b map[string]any) any {
	if a != nil {
		return a
	}
	if b != nil {
		return b
	}
	return nil
}
