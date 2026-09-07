// Package eval scores RAG retrieval and answer-grounding quality against a
// labeled golden set. Every metric here is a pure function over data the
// caller supplies (retrieved keys, a golden set, canned AskResults) — no
// live model or database dependency — so a quality regression can gate CI
// the same way any other test does, and a change like "did reranking
// actually help" is measurable rather than a vibe.
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// CitationKey returns the "type:id" identity a RetrievalCase.Relevant entry
// is expected to match — the same (SourceType, SourceID) pair fuseRRF dedupes
// citations by, so a golden set and a live retrieval result speak the same
// language.
func CitationKey(c rag.Citation) string {
	return c.SourceType + ":" + c.SourceID
}

// RecallAtK reports the fraction of relevant keys present anywhere in the
// first k of retrieved (order past k doesn't matter; duplicates in retrieved
// are harmless). An empty relevant set defines recall as 1 — nothing to
// find, nothing missed — rather than 0 or NaN, so it averages cleanly across
// a golden set that mixes retrieval and refusal-only cases.
func RecallAtK(retrieved []string, relevant []string, k int) float64 {
	if len(relevant) == 0 {
		return 1
	}
	if k > len(retrieved) {
		k = len(retrieved)
	}
	top := make(map[string]bool, k)
	for _, key := range retrieved[:k] {
		top[key] = true
	}
	hits := 0
	for _, r := range relevant {
		if top[r] {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// ReciprocalRank returns 1/rank of the first relevant key in retrieved
// (1-based), or 0 if none of relevant appears at all. Rewards a system that
// puts the right answer first, not just somewhere in the top k the way
// RecallAtK does — the two catch different regressions (RecallAtK: "did we
// lose it entirely"; ReciprocalRank: "did we bury it").
func ReciprocalRank(retrieved []string, relevant []string) float64 {
	if len(relevant) == 0 {
		return 1
	}
	want := make(map[string]bool, len(relevant))
	for _, r := range relevant {
		want[r] = true
	}
	for i, key := range retrieved {
		if want[key] {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// GroundedAnswerRate is a faithfulness proxy: the fraction of non-refusal
// answers in results that cite at least one source. It cannot tell whether a
// citation actually supports what the answer claims — that needs semantic
// judgment a pure function doesn't have — but a non-refusal answer with ZERO
// citations is a strong, cheaply-detectable signal of exactly the failure
// mode rag.Orchestrator's citedOnly filter exists to surface: the model said
// something the retrieved context didn't actually ground. Refusals are
// excluded from the denominator — they correctly have no citations, and
// counting them would inflate the score by rewarding refusing more.
func GroundedAnswerRate(results []rag.AskResult, refusalPhrase string) float64 {
	total, grounded := 0, 0
	for _, r := range results {
		if strings.Contains(r.Answer, refusalPhrase) {
			continue
		}
		total++
		if len(r.Citations) > 0 {
			grounded++
		}
	}
	if total == 0 {
		return 1
	}
	return float64(grounded) / float64(total)
}

// RetrievalCase is one labeled golden-set question.
type RetrievalCase struct {
	Question string   `json:"question"`
	Relevant []string `json:"relevant"` // CitationKey-shaped: "type:id"
}

// RetrieveFunc is the injectable callable under test: how a caller turns a
// question into ranked citations. Deliberately minimal so it can wrap
// anything — a raw rag.Corpus, an Orchestrator's retrieval step, or a canned
// fixture — without eval depending on any of their concrete types.
type RetrieveFunc func(ctx context.Context, question string) ([]rag.Citation, error)

// CaseResult is one golden-set question's scored outcome.
type CaseResult struct {
	Question       string
	RecallAtK      float64
	ReciprocalRank float64
	Err            error // set when RetrieveFunc failed; both scores are 0
}

// Report aggregates CaseResults across a golden set.
type Report struct {
	K          int
	Cases      []CaseResult
	MeanRecall float64
	MeanMRR    float64
}

// Run scores every case in golden against retrieve, returning a Report. A
// per-case retrieval error is recorded as a zero-scored CaseResult rather
// than aborting the run, so one broken fixture question doesn't hide every
// other case's result — the same reasoning ingest.IngestFS uses for
// per-file failures.
func Run(ctx context.Context, golden []RetrievalCase, k int, retrieve RetrieveFunc) Report {
	rep := Report{K: k, Cases: make([]CaseResult, len(golden))}
	var recallSum, mrrSum float64
	for i, c := range golden {
		cites, err := retrieve(ctx, c.Question)
		if err != nil {
			rep.Cases[i] = CaseResult{Question: c.Question, Err: err}
			continue
		}
		keys := make([]string, len(cites))
		for j, cite := range cites {
			keys[j] = CitationKey(cite)
		}
		cr := CaseResult{
			Question:       c.Question,
			RecallAtK:      RecallAtK(keys, c.Relevant, k),
			ReciprocalRank: ReciprocalRank(keys, c.Relevant),
		}
		rep.Cases[i] = cr
		recallSum += cr.RecallAtK
		mrrSum += cr.ReciprocalRank
	}
	if n := len(golden); n > 0 {
		rep.MeanRecall = recallSum / float64(n)
		rep.MeanMRR = mrrSum / float64(n)
	}
	return rep
}

// Worst returns the n cases with the lowest RecallAtK (ties broken by lowest
// ReciprocalRank), for pointing a failing CI gate at what actually regressed
// instead of just a mean. A retrieval error sorts as the worst possible case.
func (r Report) Worst(n int) []CaseResult {
	sorted := make([]CaseResult, len(r.Cases))
	copy(sorted, r.Cases)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := sorted[i], sorted[j]
		if (si.Err != nil) != (sj.Err != nil) {
			return si.Err != nil
		}
		if si.RecallAtK != sj.RecallAtK {
			return si.RecallAtK < sj.RecallAtK
		}
		return si.ReciprocalRank < sj.ReciprocalRank
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n]
}

// testingT is the subset of *testing.T the CI-gate helpers need, defined at
// point of use so eval stays a plain library dependency of a test rather than
// requiring "testing" itself to be imported by non-test callers.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// AssertMinRecall fails t when report.MeanRecall drops below min, printing
// the worst-scoring cases so the failure points at what regressed. Wire a
// real (or fixture) RetrieveFunc, run it against a golden set, and call this
// in a test — that test is the CI quality gate.
func AssertMinRecall(t testingT, report Report, min float64) {
	t.Helper()
	if report.MeanRecall < min {
		t.Fatalf("mean recall@%d = %.3f, want >= %.3f\nworst cases:\n%s",
			report.K, report.MeanRecall, min, formatWorst(report.Worst(3)))
	}
}

// AssertMinMRR fails t when report.MeanMRR drops below min. See AssertMinRecall.
func AssertMinMRR(t testingT, report Report, min float64) {
	t.Helper()
	if report.MeanMRR < min {
		t.Fatalf("mean MRR = %.3f, want >= %.3f\nworst cases:\n%s",
			report.MeanMRR, min, formatWorst(report.Worst(3)))
	}
}

func formatWorst(cases []CaseResult) string {
	var b strings.Builder
	for _, c := range cases {
		if c.Err != nil {
			fmt.Fprintf(&b, "  %q: retrieval error: %v\n", c.Question, c.Err)
			continue
		}
		fmt.Fprintf(&b, "  %q: recall=%.2f mrr=%.2f\n", c.Question, c.RecallAtK, c.ReciprocalRank)
	}
	return b.String()
}

// LoadGoldenJSONL reads newline-delimited RetrievalCase JSON objects from r —
// one case per line, blank lines skipped. JSONL rather than a single JSON
// array so a golden set can grow by appending lines and a diff shows exactly
// which case changed.
func LoadGoldenJSONL(r io.Reader) ([]RetrievalCase, error) {
	var cases []RetrievalCase
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c RetrievalCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return cases, nil
}
