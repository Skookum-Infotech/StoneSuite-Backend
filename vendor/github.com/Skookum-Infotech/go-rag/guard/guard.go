// Package guard scans arbitrary text — retrieved chunks, conversation
// history, or freshly parsed document text — for PII and prompt-injection
// signals. It is deliberately policy-free: a Finding says what was detected
// and where, never what to do about it (block, redact, flag for review,
// log only). Callers decide what a risk level means for their product.
//
// Detection here is pattern-based, not a model call: cheap enough to run
// over every retrieved chunk and every conversation turn on every ask
// without adding meaningful latency or cost. That cheapness is also its
// limit — these are known-shape/known-phrase detectors, not a general
// classifier, so a Finding is a signal to weigh, never a safety guarantee.
package guard

// Kind identifies what a Finding detected.
type Kind string

const (
	KindCreditCard      Kind = "credit_card"
	KindSSN             Kind = "ssn"
	KindPromptInjection Kind = "prompt_injection"
)

// Finding is one detected signal within a scanned text.
type Finding struct {
	Kind Kind
	// Start, End are byte offsets into the scanned text — [Start, End).
	Start, End int
	// Excerpt is the matched substring itself, for a human reviewer or an
	// audit log. Callers that must not persist raw PII should use Redact
	// instead of logging Excerpt for a KindCreditCard/KindSSN Finding.
	Excerpt string
}

// ScanAll runs every detector in this package over text and returns every
// Finding, in the order the underlying scans produce them (PII first, then
// injection) — a convenience for a caller that wants both signals over one
// piece of text without composing the individual Scan* calls itself.
func ScanAll(text string) []Finding {
	findings := ScanPII(text)
	findings = append(findings, ScanInjection(text)...)
	return findings
}
