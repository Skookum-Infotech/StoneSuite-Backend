package index

import (
	"strings"

	"github.com/Skookum-Infotech/go-rag/guard"
)

// injectionRedaction replaces a span of record text that reads as an
// instruction aimed at the model. It is itself plain, harmless prose, so the
// record still embeds and reads sensibly around it.
const injectionRedaction = "[removed: text that looked like an instruction to the assistant]"

// screenRecordText defuses prompt-injection phrases in a record's rendered text
// before it is embedded, and returns the findings so the caller can log them.
//
// Why here: anyone who can type into a record can write text that later reaches
// the model as retrieved context. The clearest case is a customer-portal order,
// whose special-instructions text becomes the Sales Order's memo and is indexed
// like any staff-typed field. The orchestrator already fences retrieved text as
// data (rag.SourceDataRule), but a small self-hosted model can still be talked
// out of it, so the instruction-shaped phrases are removed at the one choke
// point every record passes through on its way into rag_chunks.
//
// Policy, deliberately: redact and keep indexing, never skip the record. Skipping
// would let a customer make their own order invisible to the assistant by
// pasting a phrase, and a false positive on ordinary prose (a builder writing
// "you are now free to start") should cost one phrase, not the whole record.
// guard's patterns are known phrases, not a classifier: this narrows the attack
// surface, it does not close it.
func screenRecordText(content string) (string, []guard.Finding) {
	findings := guard.ScanInjection(content)
	if len(findings) == 0 {
		return content, nil
	}
	// Findings arrive sorted by Start; two phrases can overlap, in which case
	// the later one only extends the span already redacted.
	var b strings.Builder
	pos := 0
	for _, f := range findings {
		if f.Start < pos {
			if f.End > pos {
				pos = f.End
			}
			continue
		}
		b.WriteString(content[pos:f.Start])
		b.WriteString(injectionRedaction)
		pos = f.End
	}
	b.WriteString(content[pos:])
	return b.String(), findings
}

// findingPhrases lists the distinct, lower-cased phrases matched, for the log.
// The set is guard's fixed phrase list, so it carries no customer text.
func findingPhrases(findings []guard.Finding) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range findings {
		p := strings.ToLower(f.Excerpt)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
