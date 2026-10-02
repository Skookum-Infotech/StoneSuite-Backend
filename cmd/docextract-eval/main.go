// Command docextract-eval runs the parser-only extraction over the golden
// fixtures in testdata/docextract/<type>/ and prints accuracy numbers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"stonesuite-backend/docextract"
)

const percent = 100.0

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

type expectedLine struct {
	SKU         string `json:"sku"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
}

type expectedDoc struct {
	Header map[string]string `json:"header"`
	Lines  []expectedLine    `json:"lines"`
}

type tally struct {
	headerHit, headerTotal int
	lineHit, lineTotal     int
	parserOK, docs         int
}

func main() {
	dir := flag.String("dir", "testdata/docextract", "golden fixture root")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "docextract-eval:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	typeDirs, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read %s: %w", root, err)
	}
	for _, td := range typeDirs {
		if !td.IsDir() || docextract.DocType(td.Name()) != docextract.DocTypeSalesOrder {
			continue
		}
		t, err := evalDir(filepath.Join(root, td.Name()), docextract.DocType(td.Name()))
		if err != nil {
			return err
		}
		report(td.Name(), t)
	}
	return nil
}

func evalDir(dir string, dt docextract.DocType) (tally, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.expected.json"))
	if err != nil {
		return tally{}, fmt.Errorf("glob %s: %w", dir, err)
	}
	sort.Strings(files)
	var t tally
	for _, ef := range files {
		base := strings.TrimSuffix(ef, ".expected.json")
		doc := firstExisting(base+".pdf", base+".docx")
		if doc == "" {
			continue
		}
		exp, err := loadExpected(ef)
		if err != nil {
			return tally{}, err
		}
		b, err := os.ReadFile(doc)
		if err != nil {
			return tally{}, fmt.Errorf("read %s: %w", doc, err)
		}
		t.docs++
		res, _, xerr := docextract.Extract(context.Background(), b, dt, docextract.Options{})
		scoreDoc(&t, filepath.Base(doc), exp, res, xerr)
	}
	return t, nil
}

func scoreDoc(t *tally, name string, exp expectedDoc, res docextract.Result, xerr error) {
	t.headerTotal += len(exp.Header)
	t.lineTotal += len(exp.Lines)
	if xerr != nil {
		fmt.Printf("  %s: extract failed: %v\n", name, xerr)
		return
	}
	got := headerValues(res.Header)
	for k, want := range exp.Header {
		if got[k] == want {
			t.headerHit++
		} else {
			fmt.Printf("  %s: header %s = %q, want %q\n", name, k, got[k], want)
		}
	}
	for _, el := range exp.Lines {
		if hasLine(res.Lines, el) {
			t.lineHit++
		} else {
			fmt.Printf("  %s: line not found: %s / %s\n", name, el.SKU, el.Description)
		}
	}
	if len(res.Unresolved) == 0 {
		t.parserOK++
	}
}

func report(name string, t tally) {
	fmt.Printf("%s: %d fixtures\n", name, t.docs)
	fmt.Printf("  header exact-match: %.1f%% (%d/%d)\n", pct(t.headerHit, t.headerTotal), t.headerHit, t.headerTotal)
	fmt.Printf("  line recall:        %.1f%% (%d/%d)\n", pct(t.lineHit, t.lineTotal), t.lineHit, t.lineTotal)
	fmt.Printf("  parser-only success: %.1f%% (%d/%d)\n", pct(t.parserOK, t.docs), t.parserOK, t.docs)
	fmt.Println("  LLM-call rate:      0.0% (eval runs with no LLM)")
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return percent * float64(a) / float64(b)
}

func loadExpected(path string) (expectedDoc, error) {
	var e expectedDoc
	b, err := os.ReadFile(path)
	if err != nil {
		return e, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("parse %s: %w", path, err)
	}
	return e, nil
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func headerValues(h docextract.Header) map[string]string {
	return map[string]string{
		"po_number": h.PONumber.Value, "order_date": h.OrderDate.Value, "delivery_date": h.DeliveryDate.Value,
		"customer_name": h.CustomerName.Value, "payment_terms": h.PaymentTerms.Value,
		"subtotal": h.Subtotal.Value, "tax": h.Tax.Value, "shipping": h.Shipping.Value,
		"discount": h.Discount.Value, "total": h.Total.Value, "currency": h.Currency.Value,
	}
}

func norm(s string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), " "), " ")
}

func hasLine(lines []docextract.Line, want expectedLine) bool {
	for _, l := range lines {
		if l.SKU.Value == want.SKU && norm(l.Description.Value) == norm(want.Description) && l.Amount.Value == want.Amount {
			return true
		}
	}
	return false
}
