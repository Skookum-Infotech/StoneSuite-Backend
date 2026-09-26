package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// printTable writes a compact id/pass/ttft/total/first-failing-check table
// to stdout, in the order results were run.
func printTable(results []caseResult) {
	fmt.Printf("%-28s %-5s %8s %8s  %s\n", "ID", "PASS", "TTFT", "TOTAL", "FIRST FAILING CHECK")
	for _, r := range results {
		status := "FAIL"
		if r.Pass {
			status = "PASS"
		}
		fmt.Printf("%-28s %-5s %7dms %7dms  %s\n", r.Case.ID, status, r.TTFTMillis, r.TotalMillis, r.FirstFail)
	}
}

// printSummary writes overall and per-category pass rate and p50/p90
// latency to stdout.
func printSummary(results []caseResult) {
	overall := newLatencySet()
	byCategory := map[string]*latencySet{}
	var categories []string

	for _, r := range results {
		overall.add(r)
		ls, ok := byCategory[r.Case.Category]
		if !ok {
			ls = newLatencySet()
			byCategory[r.Case.Category] = ls
			categories = append(categories, r.Case.Category)
		}
		ls.add(r)
	}
	sort.Strings(categories)

	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("Overall: %s\n", overall.String())
	for _, cat := range categories {
		fmt.Printf("  %-16s %s\n", cat, byCategory[cat].String())
	}
}

// latencySet is one group's (overall or one category's) pass/fail and
// latency sample accumulator.
type latencySet struct {
	total       int
	pass        int
	ttftSamples []time.Duration
	totSamples  []time.Duration
}

func newLatencySet() *latencySet { return &latencySet{} }

func (l *latencySet) add(r caseResult) {
	l.total++
	if r.Pass {
		l.pass++
	}
	if r.TTFTMillis > 0 {
		l.ttftSamples = append(l.ttftSamples, time.Duration(r.TTFTMillis)*time.Millisecond)
	}
	if r.TotalMillis > 0 {
		l.totSamples = append(l.totSamples, time.Duration(r.TotalMillis)*time.Millisecond)
	}
}

func (l *latencySet) String() string {
	rate := 0.0
	if l.total > 0 {
		rate = 100 * float64(l.pass) / float64(l.total)
	}
	return fmt.Sprintf("%d/%d passed (%.0f%%)  ttft p50=%dms p90=%dms  total p50=%dms p90=%dms",
		l.pass, l.total, rate,
		percentileMillis(l.ttftSamples, 50), percentileMillis(l.ttftSamples, 90),
		percentileMillis(l.totSamples, 50), percentileMillis(l.totSamples, 90),
	)
}

// writeResultsJSON writes the full result set to path as JSON.
func writeResultsJSON(path string, results []caseResult) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create output file %s: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", path, closeErr)
		}
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return fmt.Errorf("write output file %s: %w", path, err)
	}
	return nil
}
