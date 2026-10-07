package docextract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Extraction methods.
const (
	MethodParser    = "parser"
	MethodParserLLM = "parser+llm"
)

const (
	defaultLLMTimeout  = 30 * time.Second
	fingerprintHexLen  = 16
	reconcileFieldSkip = 0
)

// ReconcileFunc is the arithmetic self-check; tests and the eval harness may
// replace it.
type ReconcileFunc func(lineAmounts []Cents, subtotal, tax, shipping, discount, total Cents) []CheckResult

// Options configures Extract.
type Options struct {
	Limits          Limits
	OwnCompanyNames []string
	LLM             LLM // nil disables the fallback
	TokenBudget     int
	LLMTimeout      time.Duration
	ReconcileFn     ReconcileFunc // nil means Reconcile
}

// Extract runs the full pipeline: sniff, parse, prune, classify, header, table,
// reconcile, required-field check and (only when needed) the LLM fallback.
// Input problems are *InputError; a parser panic is recovered as corrupt.
func Extract(ctx context.Context, b []byte, docType DocType, opts Options) (res Result, method string, err error) {
	defer func() {
		if x := recover(); x != nil {
			res, method = Result{}, ""
			err = newInputError(FailCorrupt, fmt.Sprintf("extraction panic: %v", x))
		}
	}()
	if docType != DocTypeSalesOrder {
		return Result{}, "", newInputError(FailUnsupportedType, "document type "+string(docType)+" is not supported yet")
	}
	lim := opts.Limits.withDefaults()
	if int64(len(b)) > lim.MaxBytes {
		return Result{}, "", newInputError(FailTooLarge, fmt.Sprintf("%d bytes exceeds the cap of %d", len(b), lim.MaxBytes))
	}
	kind, err := Sniff(b)
	if err != nil {
		return Result{}, "", err
	}
	res = Result{DocType: docType}
	var pages []PageRows
	switch kind {
	case KindPDF:
		pr, perr := ParsePDF(ctx, b, lim)
		if perr != nil {
			return Result{}, "", perr
		}
		pages, res.Signed, res.Restricted, res.Warnings = pr.Pages, pr.Signed, pr.Restricted, pr.Warnings
	default:
		if pages, err = ParseDOCX(b); err != nil {
			return Result{}, "", err
		}
	}
	res.Pages = PrunePages(pages)
	if err := ctx.Err(); err != nil {
		return Result{}, "", fmt.Errorf("extract cancelled: %w", err)
	}
	if err := buildSalesOrder(&res, lim, opts); err != nil {
		return Result{}, "", err
	}
	method = MethodParser
	// The model only fills gaps in something that is recognisably an order.
	// Asked about a document that isn't one (a spec sheet, a timesheet) it
	// can only invent: it once returned a form's section heading as both the
	// customer and the PO number.
	if keys := salesOrderLLMKeys(&res); len(res.Unresolved) > 0 && len(keys) > 0 && opts.LLM != nil && !notRecognized(&res) {
		if applied := runLLM(ctx, &res, keys, opts); applied {
			method = MethodParserLLM
			res.Unresolved = salesOrderUnresolved(&res)
		}
	}
	if notRecognized(&res) {
		res.Warnings = append(res.Warnings, WarnNotRecognized)
	}
	// Empty, never null, on the wire: clients iterate these without a guard.
	if res.Lines == nil {
		res.Lines = []Line{}
	}
	if res.Pages == nil {
		res.Pages = []PageRows{}
	}
	return res, method, nil
}

// notRecognized reports a document with no title class, no line table, no PO
// number and no customer: almost certainly not a purchase order at all.
func notRecognized(res *Result) bool {
	return res.ClassifiedAs == "" && len(res.Lines) == 0 &&
		!res.Header.PONumber.Found() && !res.Header.CustomerName.Found()
}

// buildSalesOrder runs the deterministic stages over res.Pages.
func buildSalesOrder(res *Result, lim Limits, opts Options) error {
	res.ClassifiedAs = Classify(res.Pages)
	if ClassMismatch(res.DocType, res.ClassifiedAs) {
		res.Warnings = append(res.Warnings, WarnDocLooksLike+res.ClassifiedAs)
	}
	tbl, err := parseTable(res.Pages, lim)
	if err != nil {
		return err
	}
	if !tbl.Found {
		res.Warnings = append(res.Warnings, WarnNoLineTable)
	}
	ho := parseHeader(res.Pages, tbl.Spans, HeaderOptions{OwnCompanyNames: opts.OwnCompanyNames})
	res.Header = ho.Header
	res.Warnings = append(res.Warnings, ho.Warnings...)
	res.Lines = tbl.Lines
	res.Warnings = append(res.Warnings, applyCharges(&res.Header, tbl.Charges)...)
	res.Revision = DetectRevision(res.Pages)
	res.QuoteRef = DetectQuoteRef(res.Pages)

	rec := opts.ReconcileFn
	if rec == nil {
		rec = Reconcile
	}
	var amounts []Cents
	for _, l := range res.Lines {
		if l.Amount.Found() && !hasFlag(l.Flags, FlagAmountUnparsed) {
			amounts = append(amounts, l.AmountCents)
		}
	}
	h := res.Header
	res.Checks = rec(amounts, fieldCents(h.Subtotal), fieldCents(h.Tax), fieldCents(h.Shipping), fieldCents(h.Discount), fieldCents(h.Total))

	res.Unresolved = salesOrderUnresolved(res)
	res.Warnings = append(res.Warnings, salesOrderRecommendedWarnings(res)...)
	res.Injection = ScanInjection(fullText(res.Pages))
	if len(res.Injection) > 0 {
		res.Warnings = append(res.Warnings, WarnInjectionPhrases)
	}
	res.LayoutFingerprint = fingerprint(ho.Keys, tbl.Roles)
	res.residualSpans = tbl.Spans
	return nil
}

// fieldCents parses a normalised money field value; unparseable is zero.
func fieldCents(f Field) Cents {
	if !f.Found() {
		return reconcileFieldSkip
	}
	c, issue := ParseMoney(f.Value)
	if !NumberFlagsUsable(issue) {
		return reconcileFieldSkip
	}
	return c
}

// fullText joins every kept row.
func fullText(pages []PageRows) string {
	var b strings.Builder
	for _, p := range pages {
		for _, r := range p.Rows {
			b.WriteString(r.Text())
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// fingerprint hashes the header vocabulary and column order.
func fingerprint(keys, roles []string) string {
	k := append([]string(nil), keys...)
	sort.Strings(k)
	sum := sha256.Sum256([]byte(strings.Join(k, ",") + "|" + strings.Join(roles, ",")))
	return hex.EncodeToString(sum[:])[:fingerprintHexLen]
}

// residualText is the header-region text (outside the line table) with rows
// that contain injection phrases removed, so they can never ground a value.
func residualText(pages []PageRows, spans map[int][2]int) string {
	var b strings.Builder
	for _, p := range pages {
		sp, has := spans[p.Page]
		for i, r := range p.Rows {
			if has && i >= sp[0] && i <= sp[1] {
				continue
			}
			text := r.Text()
			if len(ScanInjection(text)) > 0 {
				continue
			}
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// runLLM asks the fallback model for the missing keys; it reports whether any
// field was applied. Failures only add a warning.
func runLLM(ctx context.Context, res *Result, keys []string, opts Options) bool {
	residual := residualText(res.Pages, res.residualSpans)
	system, user, err := BuildPrompt(residual, keys, opts.TokenBudget)
	if err != nil {
		if errors.Is(err, ErrContextBudget) {
			res.Warnings = append(res.Warnings, WarnContextBudget)
		}
		return false
	}
	timeout := opts.LLMTimeout
	if timeout <= 0 {
		timeout = defaultLLMTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := opts.LLM.Generate(cctx, system, user)
	if err != nil {
		res.Warnings = append(res.Warnings, WarnAIUnavailable)
		return false
	}
	applied, err := ApplyLLM(res, raw, residual)
	if err != nil {
		res.Warnings = append(res.Warnings, WarnAIUnavailable)
		return false
	}
	// A customer PO names us as the vendor; the model must not put us back as the customer.
	if slices.Contains(applied, KeyCustomerName) && isOwnCompany(res.Header.CustomerName.Value, opts.OwnCompanyNames) {
		res.Header.CustomerName = notFound()
		applied = slices.DeleteFunc(applied, func(k string) bool { return k == KeyCustomerName })
	}
	return len(applied) > 0
}
